package main

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/cheggaaa/pb/v3"
	"github.com/victoriametrics/vmctl/influx"
	"github.com/victoriametrics/vmctl/vm"
)

type influxProcessor struct {
	ic *influx.Client
	im *vm.Importer
	cc int
}

func newInfluxProcessor(ic *influx.Client, im *vm.Importer, cc int) *influxProcessor {
	if cc < 1 {
		cc = 1
	}
	return &influxProcessor{
		ic: ic,
		im: im,
		cc: cc,
	}
}

func (ip *influxProcessor) run() error {
	series, err := ip.ic.Explore()
	if err != nil {
		return fmt.Errorf("explore query failed: %s", err)
	}
	if len(series) < 1 {
		return fmt.Errorf("found no timeseries to export")
	}

	question := fmt.Sprintf("Found %d timeseries to import. Continue?", len(series))
	if !prompt(question) {
		return nil
	}

	bar := pb.StartNew(len(series))
	seriesCh := make(chan *influx.Series)
	errCh := make(chan error)

	var wg sync.WaitGroup
	wg.Add(ip.cc)
	for i := 0; i < ip.cc; i++ {
		go func() {
			defer wg.Done()
			for s := range seriesCh {
				if err := ip.do(s); err != nil {
					errCh <- err
					return
				}
				bar.Increment()
			}
		}()
	}

	// any error breaks the import
	for _, s := range series {
		select {
		case infErr := <-errCh:
			return fmt.Errorf("influx error: %s", infErr)
		case vmErr := <-ip.im.Errors():
			var errTS string
			for _, ts := range vmErr.Batch {
				errTS += fmt.Sprintf("%s for timestamps range %d - %d\n",
					ts.String(), ts.Timestamps[0], ts.Timestamps[len(ts.Timestamps)-1])
			}
			return fmt.Errorf("Import process failed for \n%sWith error: %s", errTS, vmErr.Err)
		case seriesCh <- s:
		}
	}

	close(seriesCh)
	wg.Wait()
	ip.im.Close()
	bar.Finish()
	log.Println("Import finished!")
	log.Print(ip.im.Stats())
	return nil
}

func (ip *influxProcessor) do(s *influx.Series) error {
	cr, err := ip.ic.FetchDataPoints(s)
	if err != nil {
		return fmt.Errorf("failed to fetch datapoints: %s", err)
	}
	name := fmt.Sprintf("%s_%s", s.Measurement, s.Field)
	labels := make([]vm.LabelPair, len(s.LabelPairs))
	for i, lp := range s.LabelPairs {
		labels[i] = vm.LabelPair{
			Name:  lp.Name,
			Value: lp.Value,
		}
	}

	for {
		time, values, err := cr.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		ip.im.Input() <- &vm.TimeSeries{
			Name:       name,
			LabelPairs: labels,
			Timestamps: time,
			Values:     values,
		}
	}
}