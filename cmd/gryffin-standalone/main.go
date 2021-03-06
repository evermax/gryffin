// Copyright 2015, Yahoo Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/yahoo/gryffin"
	"github.com/yahoo/gryffin/data"
	"github.com/yahoo/gryffin/fuzzer/arachni"
	"github.com/yahoo/gryffin/fuzzer/sqlmap"
	"github.com/yahoo/gryffin/renderer"
)

var method = flag.String("method", "GET", "the HTTP method for the request.")
var url string
var body = flag.String("data", "", "the data used in a (POST) request.")

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\tgryffin-standalone [flags] seed-url\n")
	fmt.Fprintf(os.Stderr, "Flags:\n")
	flag.PrintDefaults()
}

// THIS IS BAD CODE per https://blog.golang.org/pipelines, and is created for prototyping.
// In production, we will move the channels out and use message queue instead.
func linkChannels(s *gryffin.Scan) {

	var wg sync.WaitGroup

	chanStart := make(chan *gryffin.Scan, 10)
	chanRateLimit := make(chan *gryffin.Scan, 10)
	chanCrawl := make(chan *gryffin.Scan, 10)
	chanFuzz := make(chan *gryffin.Scan, 10)
	// defer close(chanStart)
	defer close(chanRateLimit)
	defer close(chanCrawl)
	defer close(chanFuzz)

	// TODO - name all of these functions.

	// Crawl -> Filter by Domain / Rate Limit
	go func() {

		for scan := range chanCrawl {
			// scan := <-chanCrawl
			r := &renderer.PhantomJSRenderer{Timeout: 10}
			// r := &renderer.NoScriptRenderer{}
			scan.CrawlAsync(r)

			go func() {
				if s := <-r.GetRequestBody(); s != nil {
					chanFuzz <- s
				} else {
					wg.Done()
				}

			}()

			go func() {
				isUnique := false
				for newScan := range r.GetLinks() {

					// do the evaluation once only.
					isUnique = isUnique || scan.IsUnique()

					if isUnique {

						if ok := newScan.ApplyLinkRules(); ok {
							wg.Add(1)
							chanRateLimit <- newScan
						}
					}

				}

				scan.Logm("Get Links", "Finished")

			}()

		}

	}()

	go func() {
		for scan := range chanFuzz {

			go func() {
				f := &arachni.Fuzzer{}
				f.Fuzz(scan)
				wg.Done()
			}()
			go func() {
				f := &sqlmap.Fuzzer{}
				f.Fuzz(scan)
			}()
			// Finished crawling a link...
		}

	}()

	// Rate Limit -> Crawl
	go func() {
		for scan := range chanRateLimit {
			if delay := scan.RateLimit(); delay != 0 {
				go func() {
					time.Sleep(time.Duration(delay) * time.Second)
					chanRateLimit <- scan
				}()
				// TODO queue it again.
				continue
			}
			chanCrawl <- scan
		}
	}()

	// Start, Poke -> RateLimit
	go func() {
		for scan := range chanStart {
			err := scan.Poke(&http.Client{})
			if err != nil {
				// if scan.HitCount <= 5 {
				// 	go func() {
				// 		time.Sleep(5 * time.Second)
				// 		chanStart <- scan
				// 	}()
				// }
				// continue
			}
			chanRateLimit <- scan
		}
	}()

	chanStart <- s
	close(chanStart)
	wg.Add(1)
	wg.Wait()
}

func main() {

	flag.Usage = usage
	flag.Parse()

	switch flag.NArg() {
	case 1:
		url = flag.Arg(0)
	default:
		usage()
		return

	}

	// TCP port listening messages.
	tcpout, err := net.Dial("tcp", "localhost:5000")
	if err != nil {
		fmt.Println("Cannot establish tcp connection to log listener.")
	}

	w := io.MultiWriter(os.Stdout, tcpout)

	scan := gryffin.NewScan(*method, url, *body, data.NewMemoryStore(), w)
	scan.Logm("Main", "Started")

	linkChannels(scan)

}
