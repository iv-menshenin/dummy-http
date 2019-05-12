package main

import (
	"flag"
	"net/http"
	"fmt"
	"io/ioutil"
	"net"
	"sync"
	"encoding/json"
	"bufio"
	"strconv"
	"net/url"
	"strings"
	"io"
)

type(
	httpHandler struct {
		mux sync.Mutex
		stopper chan interface{}
		listener net.Listener
		repeatTo []net.Conn
	}
)

var(
	addr      *string
	hello     *string
	repeat    *string
	repeater  *string
	sendTo    *string
	trailLine  = "--------------------------------------------------------------------------------"
)

func main(){
	var handler httpHandler
	addr = flag.String("addr", "localhost:8080", "Specify host/port for listening")
	hello = flag.String("hello", "Hello world!", "Specify the response data")
	repeat = flag.String("repeat", "", "Repeat requests to socket")
	repeater = flag.String("repeater", "", "Get requests from socket and sent it forward")
	sendTo = flag.String("send_to", "", "Where to send message")
	flag.Parse()
	if *repeat != "" {
		handler.initSocket()
	}
	if *repeater == "" {
		println(fmt.Sprintf("Listen at %s. Returning data: %s", *addr, *hello))
		err := http.ListenAndServe(*addr, &handler)
		if err != nil {
			panic(err)
		}
	} else {
		handler.establishReaderSocket()
	}
}

type(
	requestCopy struct {
		Method string
		Header http.Header
		Body []byte
		p *bool
	}
)

func (r requestCopy)Read(p []byte) (n int, err error){
	if *r.p {
		return 0, io.EOF
	}
	n = copy(p, r.Body)
	*r.p = true
	return
}

func (r requestCopy)Close() error{
	return nil
}

func (s *httpHandler)establishReaderSocket(){
	conn, err := net.Dial("tcp", *repeater)
	if err != nil {
		panic(err)
	}
	for {
		reader := bufio.NewReader(conn)
		headText, err := reader.ReadString('\n')
		if err != nil {
			panic(err)
		}
		l, err := strconv.ParseInt(strings.TrimSpace(headText), 10, 64)
		if err != nil {
			panic(err)
		}

		b := make([]byte, l)
		pos := 0
		for l > 0 {
			cnt, err := reader.Read(b[pos:])
			if err != nil {
				panic(err)
			}
			pos += cnt
			l -= int64(cnt)
		}
		println(fmt.Sprintf("Message from server: %s", string(b)))
		var r requestCopy
		err = json.Unmarshal(b, &r)
		r.p = new(bool)
		*r.p = false
		if err != nil {
			panic(err)
		}
		u, err := url.Parse(*sendTo)
		if err != nil {
			panic(err)
		}
		var req = new(http.Request)
		req.Body = r
		req.URL = u
		req.Header = r.Header
		req.ProtoMajor = 1
		req.ProtoMinor = 0
		req.Method = r.Method
		// req.Host = *addr
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			println(fmt.Sprintf("<%T>: %s", err, err))
		} else {
			println(fmt.Sprintf("Request status: %s", resp.Status))
		}
	}
}

func (s *httpHandler)initSocket(){
	println(fmt.Sprintf("Start listener on: %s (Socket-Repeater)", *repeat))
	var err error
	var ch = make(chan net.Conn)
	s.listener, err = net.Listen("tcp", *repeat)
	if err != nil {
		panic(err)
	}
	go func(){
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				println(fmt.Sprintf("<%T>: %s", err, err))
			} else {
				ch <- conn
			}
		}
	}()
	go func(){
		defer close(ch)
		for {
			select {
			case conn, ok := <- ch:
				if !ok {
					continue
				}
				if err != nil {
					println(fmt.Sprintf("<%T>: %s", err, err))
				} else {
					println(fmt.Sprintf("Connected: %s", conn.RemoteAddr().String()))
					func(){
						s.mux.Lock()
						defer s.mux.Unlock()
						s.repeatTo = append(s.repeatTo, conn)
					}()
				}
			case <-s.stopper: return
			}
		}

	}()
}

func (s *httpHandler)RepeatToSocket(r *http.Request, body []byte){
	s.mux.Lock()
	defer s.mux.Unlock()
	var req = requestCopy{
		Method: r.Method,
		Header: r.Header,
		Body: body,
	}
	b, err := json.Marshal(&req)
	if err != nil {
		println(fmt.Sprintf("<%T>: %s", err, err))
		return
	}
	h := []byte(fmt.Sprintf("%d\n", len(b)))
	for i := range s.repeatTo {
		s.repeatTo[i].Write(h)
		s.repeatTo[i].Write(b)
	}
}

func (s *httpHandler)ServeHTTP(w http.ResponseWriter, r *http.Request){
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
	if *repeat != "" {
		s.RepeatToSocket(r, b)
	}
	w.Write([]byte(*hello))
}