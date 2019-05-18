package main

import (
	"flag"
	"net/http"
	"fmt"
	"io/ioutil"
	"net"
	"sync"
	"bufio"
	"strconv"
	"net/url"
	"strings"
	"io"
	"bytes"
	"errors"
	"time"
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
		for{
			if func()bool{
				defer func(){
					e := recover()
					if e != nil {
						println(fmt.Sprintf("<%T>: %s", e, e))
					}
				}()
				return handler.establishReaderSocket()
			}() {
				break
			}
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

type(
	customHTTPTransport struct{
		connection net.Conn
		reader *bufio.Reader
		body []byte
	}
)

func (t *customHTTPTransport) SendCommand(cmd, expect string) (response string, attribute int64) {
	println(">>> " + cmd)
	_, err := t.connection.Write([]byte(cmd + "?"))
	if err != nil {
		panic(err)
	}
	respData, err := t.reader.ReadString('!')
	if err != nil {
		panic(err)
	}
	println("<<< " + respData)
	respData = strings.TrimSpace(respData)
	splitData := strings.Split(respData[:len(respData) - 1], " ")
	if len(splitData) > 0 {
		response = splitData[0]
	}
	if response != expect {
		panic(errors.New("unexpected response (expected " + expect + "): " + response))
	}
	if len(splitData) > 1 {
		attribute, err = strconv.ParseInt(splitData[1], 10, 64)
		if err != nil {
			panic(err)
		}
	}
	return
}

func (t *customHTTPTransport) ReceiveCommand(expect, response string) (cmd string, attribute int64) {
	request, err := t.reader.ReadString('?')
	if err != nil {
		panic(err)
	}
	println("<<< " + request)
	request = strings.TrimSpace(request)
	splitData := strings.Split(request[:len(request) - 1], " ")
	if len(splitData) > 0 {
		cmd = splitData[0]
	}
	if len(splitData) > 1 {
		attribute, err = strconv.ParseInt(splitData[1], 10, 64)
		if err != nil {
			panic(err)
		}
	}
	if cmd != expect {
		panic(errors.New("unexpected request (expected " + expect + "): " + request))
	}
	println(">>> " + response)
	_, err = t.connection.Write([]byte(response + "!"))
	if err != nil {
		panic(err)
	}
	return
}

func readFromNetwork(contentLen int64, reader *bufio.Reader, bufResp *bytes.Buffer) error {
	for{
		var tmpResp []byte
		if contentLen > 1024 {
			tmpResp = make([]byte, 1024)
		} else {
			tmpResp = make([]byte, contentLen)
		}
		n, err := reader.Read(tmpResp)
		if err != nil {
			if err != io.EOF {
				return err
			}
			time.Sleep(10)
		}
		bufResp.Write(tmpResp[:n])
		contentLen -= int64(n)
		if contentLen < 1 {
			break
		}
	}
	return nil
}

func (s *httpHandler)establishReaderSocket() (breakProcess bool) {
	s.mux.Lock()
	defer s.mux.Unlock()
	conn, err := net.Dial("tcp", *repeater)
	if err != nil {
		println(fmt.Sprintf("Critical error when connecting attempt: <%T>: %s", err, err))
		return true
	}
	transport := customHTTPTransport{
		connection: conn,
		reader: bufio.NewReader(conn),
	}
	for {

		transport.ReceiveCommand("READY", "READY")
		_, dataLen := transport.ReceiveCommand("RECEIVE", "YES")
		var buf bytes.Buffer
		if err := readFromNetwork(dataLen, transport.reader, &buf); err != nil {
			panic(err)
		}
		request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf.Bytes())))
		u, err := url.Parse(*sendTo)
		if err != nil {
			panic(err)
		}
		request.URL = u
		request.URL.Path = request.RequestURI
		request.RequestURI = ""
		resp, err := http.DefaultClient.Do(request)

		var responseData bytes.Buffer
		if err != nil {
			println(fmt.Sprintf("<%T>: %s", err, err))
			responseData.Write([]byte(fmt.Sprintf("%s %d %s\r\n\r\n", resp.Proto, 500, http.StatusText(500))))
			responseData.Write([]byte(fmt.Sprintf("<%T>: %s", err, err)))
		} else {
			println(fmt.Sprintf("Request status: %s", resp.Status))
			responseData.Write([]byte(fmt.Sprintf("%s %d %s\r\n", resp.Proto, resp.StatusCode, http.StatusText(resp.StatusCode))))
			resp.Header.WriteSubset(&responseData, map[string]bool{
				"Content-Length": true,
			})
			var body []byte
			body, _ = ioutil.ReadAll(resp.Body)
			if body != nil {
				responseData.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))))
				responseData.Write(body)
			} else {
				responseData.Write([]byte("\r\n"))
			}
		}
		transport.ReceiveCommand("RETRIEVE", fmt.Sprintf("CATCH %d", responseData.Len()))
		_, err = transport.connection.Write(responseData.Bytes())
		println("********************************************************************************")
		println(string(responseData.Bytes()))
		println("********************************************************************************")
		if err != nil {
			panic(err)
		}
		transport.ReceiveCommand("BYE", "BYE")
	}
}

func (t *customHTTPTransport) RoundTrip(r *http.Request) (resp *http.Response, err error) {

	defer func(){
		rec := recover()
		if rec != nil {
			if e, ok := rec.(error); ok {
				err = e
			} else
			if e, ok := rec.(*error); ok {
				err = *e
			} else
			if s, ok := rec.(*string); ok {
				err = errors.New(*s)
			} else {
				err = errors.New(fmt.Sprintf("<%T>: %s", rec, rec))
			}
		}
	}()

	var buf bytes.Buffer

	buf.Write([]byte(fmt.Sprintf("%s %s %s\r\n", r.Method, r.URL.Path, r.Proto)))
	r.Header.WriteSubset(&buf, nil)
	if t.body != nil {
		buf.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(t.body))))
		buf.Write(t.body)
	} else {
		buf.Write([]byte("\r\n"))
	}

	t.SendCommand("READY", "READY")
	t.SendCommand(fmt.Sprintf("RECEIVE %d", buf.Len()), "YES")

	_, err = buf.WriteTo(t.connection)
	if err != nil {
		return nil, err
	}
	var bufResp bytes.Buffer

	_, contentLen := t.SendCommand("RETRIEVE", "CATCH")
	if contentLen < 1 {
		return nil, errors.New(fmt.Sprintf("unexpected content length: %d", contentLen))
	} else {
		if err := readFromNetwork(contentLen, t.reader, &bufResp); err != nil {
			return nil, err
		}
		println("********************************************************************************")
		println(string(bufResp.Bytes()))
		println("********************************************************************************")
	}
	if bufResp.Len() == 0 {
		return nil, errors.New(fmt.Sprintf("unexpected received data length: %d", bufResp.Len()))
	}
	println("total size:", bufResp.Len())
	t.SendCommand("BYE", "BYE")

	return http.ReadResponse(bufio.NewReader(bytes.NewReader(bufResp.Bytes())), r)
}

func (s *httpHandler)RepeatToSocket(r *http.Request, body []byte) (oStatus int, oHeader http.Header, oBody []byte){
	s.mux.Lock()
	defer s.mux.Unlock()

	type(
		responseElem struct{
			response *http.Response
			err error
			idx int
		}
	)
	var(
		w sync.WaitGroup
		ch = make(chan responseElem, len(s.repeatTo))
	)
	r.RequestURI = ""
	w.Add(len(s.repeatTo))
	for i := range s.repeatTo {
		go func(idx int, reqCopy *http.Request, reqBody []byte){
			defer w.Done()
			client := http.Client{
				Transport: &customHTTPTransport{
					connection: s.repeatTo[i],
					reader: bufio.NewReader(s.repeatTo[i]),
					body: reqBody,
				},
			}
			respFromSocket, errFromSocket := client.Do(reqCopy)
			ch <- responseElem{
				response: respFromSocket,
				err:      errFromSocket,
				idx:      idx,
			}
		}(i, r, body)
	}
	w.Wait()
	close(ch)
	for r := range ch {
		if r.err != nil {
			func(conn net.Conn){
				err := conn.Close()
				if err != nil {
					println(fmt.Sprintf("<%T>: %s", err, err))
				}
			}(s.repeatTo[r.idx])
			s.repeatTo[r.idx] = nil
			return http.StatusInternalServerError, nil, []byte(fmt.Sprintf("<%T>: %s", r.err, r.err))
		}
		if oStatus < r.response.StatusCode {
			oStatus = r.response.StatusCode
			oHeader = r.response.Header
			oBody, r.err = ioutil.ReadAll(r.response.Body)
			if r.err != nil {
				return http.StatusInternalServerError, nil, []byte(fmt.Sprintf("<%T>: %s", r.err, r.err))
			}
		}
	}
	return
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
		status, headers, body := s.RepeatToSocket(r, b)
		if status == 0 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(*hello))
			return
		}
		w.WriteHeader(status)
		if headers != nil {
			for key, val := range headers {
				w.Header().Add(key, strings.Join(val, "\n"))
			}
		}
		if body != nil {
			w.Write(body)
		}
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(*hello))
	}
}