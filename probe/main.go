// A probe: does a current Go program run on the TouchPad's 2.6.35 kernel,
// and can it make a modern HTTPS connection by itself?
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"
)

func main() {
	fmt.Println("go", runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH, "cpus", runtime.NumCPU())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); time.Sleep(50 * time.Millisecond) }()
	}
	wg.Wait()
	fmt.Println("goroutines and timers: ok")
	client := &http.Client{Timeout: 20 * time.Second}
	for _, url := range os.Args[1:] {
		start := time.Now()
		resp, err := client.Get(url)
		if err != nil {
			fmt.Println("FAIL", url, err)
			continue
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		tls := "no tls"
		if resp.TLS != nil {
			tls = fmt.Sprintf("tls 0x%04x", resp.TLS.Version)
		}
		fmt.Println("ok", url, resp.Status, resp.Proto, tls, n, "bytes", time.Since(start).Round(time.Millisecond))
	}
}
