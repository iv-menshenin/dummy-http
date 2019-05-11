package main

import (
	"flag"
	"net/http"
	"fmt"
	"io/ioutil"
)

type(
	httpHandler struct {}
)

var(
	addr *string
	hello *string
	trailLine = "--------------------------------------------------------------------------------"
)

func main(){
	var handler httpHandler
	addr = flag.String("addr", "localhost:8080", "Specify host/port for listening")
	hello = flag.String("hello", "Hello world!", "Specify the response data")
	flag.Parse()
	println(fmt.Sprintf("Listen at %s. Returning data: %s", *addr, *hello))
	http.ListenAndServe(*addr, &handler)
}

func (s *httpHandler)ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		println(fmt.Sprintf("<%T>: %s", err, err))
		b = []byte{}
	}
	println(trailLine)
	println(fmt.Sprintf(
		"REQUEST FROM %s TO %s\n%s %s %s",
		r.RemoteAddr,
		r.Host,
		r.Method,
		r.URL.String(),
		r.Proto,
	))
	for k, v := range r.Header {
		g := ""
		for i := range v {
			g += "\n" + v[i]
		}
		println(fmt.Sprintf("%s: %s", k, g[1:]))
	}
	println("BODY:")
	if len(b) > 0 {
		println(string(b))
	}
	println(trailLine)
	println(fmt.Sprintf("RESPONSE:\n%s", *hello))
	println(trailLine)
	w.Write([]byte(*hello))
}