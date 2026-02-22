package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	interval := 2
	if s := os.Getenv("TICK_INTERVAL"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			interval = n
		}
	}

	fmt.Printf("ticker: printing every %ds\n", interval)
	tick := time.NewTicker(time.Duration(interval) * time.Second)
	defer tick.Stop()

	i := 0
	for range tick.C {
		i++
		fmt.Printf("example code tick #%d at %s\none\nmore\nline\nand\nanother", i, time.Now().Format(time.RFC3339))
	}
}
