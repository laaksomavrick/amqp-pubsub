package main

import (
	"fmt"
	"time"
)

func simple() {

	channel := make(chan string)

	pinger := func(ch chan string) {
		for {
			ch <- "ping"
			time.Sleep(1 * time.Second)
		}
	}

	reader := func(ch chan string) {
		for {
			select {
			case msg := <-ch:
				fmt.Printf("%s\n", msg)
			}
		}
	}

	go pinger(channel)
	go reader(channel)
	fmt.Scanln()

}
