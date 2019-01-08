package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatch"
	toml "github.com/pelletier/go-toml"
	stathat "github.com/stathat/go"
)

func main() {
	go trapSignals()
	config, err := toml.LoadFile("config.toml")
	if err != nil {
		log.Fatal(err)
	}

	ezkey := config.Get("stathat.ezkey").(string)
	sleepDur := 30 * time.Second
	if config.Has("sleep") {
		sleepDur = time.Duration(config.Get("sleep").(int64)) * time.Second
	}

	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	client := cloudwatch.New(sess)

	end := time.Now().Truncate(1 * time.Minute).Add(1 * time.Minute)
	start := end.Add(-5 * time.Minute)

	var queries []*cloudwatch.MetricDataQuery
	statNames := make(map[string]string)
	metrics := config.Get("metrics").(*toml.Tree)
	for _, key := range metrics.Keys() {
		metric := metrics.Get(key).(*toml.Tree)
		q := cloudwatch.MetricDataQuery{
			Id: aws.String(key),
			MetricStat: &cloudwatch.MetricStat{
				Metric: &cloudwatch.Metric{
					Namespace:  aws.String(metric.Get("namespace").(string)),
					MetricName: aws.String(metric.Get("name").(string)),
				},
				Period: aws.Int64(60),
				Stat:   aws.String("Average"),
			},
		}
		if metric.Has("dimension") && metric.Has("dimvalue") {
			dim := cloudwatch.Dimension{
				Name:  aws.String(metric.Get("dimension").(string)),
				Value: aws.String(metric.Get("dimvalue").(string)),
			}
			q.MetricStat.Metric.Dimensions = []*cloudwatch.Dimension{&dim}
		}
		queries = append(queries, &q)

		if metric.Has("stat_name") {
			statNames[key] = metric.Get("stat_name").(string)
		}
	}
	input := cloudwatch.GetMetricDataInput{
		StartTime:         &start,
		EndTime:           &end,
		ScanBy:            aws.String("TimestampDescending"),
		MetricDataQueries: queries,
	}

	last := make(map[string]int64)

	for {
		now := time.Now()
		end := now.Truncate(1 * time.Minute).Add(1 * time.Minute)
		start := end.Add(-5 * time.Minute)
		input.StartTime = &start
		input.EndTime = &end
		output, err := client.GetMetricData(&input)
		if err != nil {
			log.Fatal(err)
		}
		for _, result := range output.MetricDataResults {
			if len(result.Timestamps) == 0 {
				continue
			}

			stamp := result.Timestamps[0]
			if stamp == nil {
				continue
			}
			unixStamp := stamp.Unix()
			if last[*result.Id] >= unixStamp {
				continue
			}

			fmt.Printf("%s:\t%.3f @ %s\n", *result.Id, *result.Values[0], result.Timestamps[0])
			if statName, ok := statNames[*result.Id]; ok {
				stathat.PostEZValueTime(statName, ezkey, *result.Values[0], result.Timestamps[0].Unix())
			}

			last[*result.Id] = unixStamp
		}
		left := sleepDur - time.Since(now)
		time.Sleep(left)
	}
}

func trapSignals() {
	c := make(chan os.Signal, 10)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGKILL)
	for {
		x := <-c
		log.Printf("signal %s trapped.  finishing up any existing work...", x)
		stathat.WaitUntilFinished(10 * time.Second)
		log.Printf("done")
		os.Exit(0)
	}
}
