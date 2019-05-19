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
	"runtime"
	"path/filepath"
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
	trailLine  = strings.Repeat("-", 80)
	extraLine  = strings.Repeat("*", 80)
	handlerURL *url.URL
)

func main(){
	var handler httpHandler
	var w sync.WaitGroup
	handler.stopper = make(chan interface{})

	addr = flag.String("addr", "", "Specify address for listening (host:port format)")
	hello = flag.String("hello", "Hello world!", "Specify the response data string")
	repeat = flag.String("repeat", "", "Create a repeater socket and send all requests to it (IP:port format)")
	repeater = flag.String("repeater", "", "Get requests from socket and sent it forward (host:port format)")
	sendTo = flag.String("send_to", "", "Where to send message (must be whole URL e.g. http://localhost:8800)")
	flag.Parse()

	if *repeat != "" {
		w.Add(1)
		handler.initRepeaterService(func(){
			w.Done()
		})
	}
	if *repeater != "" && *addr != "" {
		panic(errors.New("you cannot specify both addr and repeater, because these are source"))
	}
	if *repeater == "" && *addr == "" {
		flag.PrintDefaults()
		return
	}
	if *repeater == "" {
		println(fmt.Sprintf("Listen at %s. Returning data: %s", *addr, *hello))
		err := http.ListenAndServe(*addr, &handler)
		if err != nil {
			panic(err)
		}
	} else {
		if *sendTo != "" {
			var err error
			handlerURL, err = url.Parse(*sendTo)
			if err != nil {
				panic(err)
			}
		}
		for{
			if func()bool{
				defer func(){
					e := recover()
					if e != nil {
						println(fmt.Sprintf("<%T>: %s\n%s\nCALLSTACK:", e, e, trailLine))
						for nn := 1; nn < 10; nn ++ {
							fncPtr, file, line, ok := runtime.Caller(nn)
							if ok {
								filePath, fileName := filepath.Split(file)
								lastDirectory := filepath.Base(filePath)
								functionName := runtime.FuncForPC(fncPtr).Name()
								println(fmt.Sprintf("%s [%d] %s/%s", functionName, line, lastDirectory, fileName))
							}
						}
					}
				}()
				return handler.initSocketReaderAndProcessRequests()
			}() {
				close(handler.stopper)
				break
			}
		}
	}
	w.Wait()
}

// Starting listener for Socket-Repeater. Is a service for transmitting a HTTP request to another host. Async.
// All routines stop with a single signal: close(httpHandler.stopper)
func (s *httpHandler) initRepeaterService(onDone func()){
	println(fmt.Sprintf("Start listener on: %s (Socket-Repeater)", *repeat))
	var err error
	var ch = make(chan net.Conn)
	if s.listener, err = net.Listen("tcp", *repeat); err != nil {
		panic(err)
	}
	// This function will be the second to complete therefore, onDone is performed
	go func(l net.Listener){
		if onDone != nil {
			defer onDone()
		}
		for {
			conn, err := l.Accept()
			if err != nil {
				println(fmt.Sprintf("<%T>: %s", err, err))
			} else {
				ch <- conn
			}
		}
	}(s.listener)
	// control over the performance of the service
	// This function will be the first to complete
	go func(l net.Listener){
		// release all after exiting
		defer close(ch)
		defer l.Close()
		for {
			select {
			case conn := <-ch:
				println(fmt.Sprintf("Connected: %s", conn.RemoteAddr().String()))
				func(){
					s.mux.Lock()
					defer s.mux.Unlock()
					s.repeatTo = append(s.repeatTo, conn)
				}()
			case <-s.stopper: return
			}
		}
	}(s.listener)
}

// The communication protocol is simple (request-response):
//     READY? - READY!
//     RECEIVE NN? - YES!
//     {the following is the transfer of the entire HTTP-request}
//     RETRIEVE? - CATCH MM!
//     {next is the transmission of the HTTP-response}
//     BYE? - BYE!
//
// There is
//     NN - number of bytes of the HTTP-request
//     MM - number of bytes of response
type(
	customHTTPTransport struct{
		connection net.Conn		// socket connection
		reader *bufio.Reader	// same socket but just reader interface
		body []byte				// the copy of the original request body
	}
)

// implements the transfer of a single command to the socket, as well as parsing and checking the correctness of the answer
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

// Implements receiving and parsing a single command from a socket, as well as checking the correctness of data exchange
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

// Implements buffered reading of data from the network of the expected number of characters
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
			// maybe the data has not reached yet, well, I'll wait
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

// Processing request. if there is a destination address, a new request is made for it.
// Otherwise, a dummy response is given.
func getAndProcessRequestFromNetwork(dataLen int64, transport *customHTTPTransport) (request *http.Request, response *http.Response, err error){
	var buf bytes.Buffer
	if err := readFromNetwork(dataLen, transport.reader, &buf); err != nil {
		panic(err)
	}
	request, err = http.ReadRequest(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		return
	}
	if handlerURL != nil {
		request.URL = handlerURL
		request.URL.Path = request.RequestURI
		request.RequestURI = ""
		requestURI := request.URL.String()
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			return
		}
		response.Header.Set("X-Request-Status", "HELLO")
		response.Header.Set("X-Request-Origin", requestURI)
	} else {
		response = new(http.Response)
		response.Body = ioutil.NopCloser(bytes.NewReader([]byte(*hello)))
		response.StatusCode = 200
		response.Status = "200 OK"
		response.Proto = "HTTP/1.0"
		response.Request = request
		response.ContentLength = int64(len([]byte(*hello)))
		response.Header = map[string][]string{
			"X-Request-Status": {"HELLO"},
			"X-Request-Origin": {"DUMMY"},
		}
	}
	return
}

// Receives one request from the service socket and executes it, giving the result back to the socket
func getAndProcessRemoteRequest(transport customHTTPTransport) {
	var responseData bytes.Buffer
	transport.ReceiveCommand("READY", "READY")
	_, dataLen := transport.ReceiveCommand("RECEIVE", "YES")
	request, response, err := getAndProcessRequestFromNetwork(dataLen, &transport)
	if err != nil {
		println(fmt.Sprintf("<%T>: %s", err, err))
		var proto string
		if request != nil {
			proto = request.Proto
		} else {
			proto = "HTTP/1.0"
		}
		responseData.Write([]byte(fmt.Sprintf("%s %d %s\r\n\r\n", proto, 500, http.StatusText(500))))
		responseData.Write([]byte(fmt.Sprintf("<%T>: %s", err, err)))
	} else {
		println(fmt.Sprintf("Request status: %s", response.Status))
		responseData.Write([]byte(fmt.Sprintf("%s %d %s\r\n", response.Proto, response.StatusCode, http.StatusText(response.StatusCode))))
		response.Header.WriteSubset(&responseData, map[string]bool{
			"Content-Length": true,
		})
		var body []byte
		body, _ = ioutil.ReadAll(response.Body)
		if body != nil {
			responseData.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))))
			responseData.Write(body)
		} else {
			responseData.Write([]byte("\r\n"))
		}
	}
	transport.ReceiveCommand("RETRIEVE", fmt.Sprintf("CATCH %d", responseData.Len()))
	_, err = transport.connection.Write(responseData.Bytes())
	println(extraLine)
	println(string(responseData.Bytes()))
	println(extraLine)
	if err != nil {
		panic(err)
	}
	transport.ReceiveCommand("BYE", "BYE")
}

// Create a connection to the service socket-repeater for receiving HTTP-requests from it.
func (s *httpHandler) initSocketReaderAndProcessRequests() (breakProcess bool) {
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
		getAndProcessRemoteRequest(transport)
	}
}

// Wrap the request. Her job is to transfer the request to the service channel as accurately as possible,
// rather than actually processing it. On the side of the creator of this socket, the request will be processed
// and the answer to it will come in the same socket.
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

	var httpRequestRawData bytes.Buffer
	httpRequestRawData.Write([]byte(fmt.Sprintf("%s %s %s\r\n", r.Method, r.URL.Path, r.Proto)))
	r.Header.WriteSubset(&httpRequestRawData, map[string]bool{
		"Content-Length": true,
	})
	if t.body != nil {
		httpRequestRawData.Write([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(t.body))))
		httpRequestRawData.Write(t.body)
	} else {
		httpRequestRawData.Write([]byte("\r\n"))
	}

	t.SendCommand("READY", "READY")
	t.SendCommand(fmt.Sprintf("RECEIVE %d", httpRequestRawData.Len()), "YES")

	_, err = httpRequestRawData.WriteTo(t.connection)
	if err != nil {
		return nil, err
	}
	var httpResponseRawData bytes.Buffer
	_, contentLen := t.SendCommand("RETRIEVE", "CATCH")
	if contentLen < 1 {
		return nil, errors.New(fmt.Sprintf("unexpected content length: %d", contentLen))
	} else {
		if err := readFromNetwork(contentLen, t.reader, &httpResponseRawData); err != nil {
			return nil, err
		}
		println(extraLine)
		println(string(httpResponseRawData.Bytes()))
		println(extraLine)
	}
	if httpResponseRawData.Len() == 0 {
		return nil, errors.New(fmt.Sprintf("unexpected received data length: %d", httpResponseRawData.Len()))
	}
	println("total size:", httpResponseRawData.Len())
	t.SendCommand("BYE", "BYE")

	return http.ReadResponse(bufio.NewReader(bytes.NewReader(httpResponseRawData.Bytes())), r)
}

// The request will be repeated for all connections, all responses will be collected and analyzed.
// One result will be selected as the resulting answer.
func (s *httpHandler) RepeatToSocket(r *http.Request, body []byte) (oStatus int, oHeader http.Header, oBody []byte){
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
		// Warning!
		// Any of these connections can be closed at any time, so we must mark such connections as complete.
		// For this purpose, we transmit error information to the channel and as soon as it is read, the link
		// to the connection will be set to nil. Such compounds will no longer be used.
		go func(idx int, reqCopy *http.Request, reqBody []byte){
			defer w.Done()
			// this is where we check if the connection still alive
			if s.repeatTo[idx] == nil {
				return
			}
			client := http.Client{
				// this is necessary so that our request goes to a sub socket
				Transport: &customHTTPTransport{
					connection: s.repeatTo[idx],
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
	// We have to wait until all the subroutines do their work, and then close the channel.
	// This will signal the HTTP response cycle that the data will no longer arrive.
	go func(){
		defer close(ch)
		w.Wait()
	}()
	for response := range ch {
		if response.err != nil {
			// It should be with minimum priority, since here, first of all, those connections that have been closed.
			// Closing connections is not an error
			func(conn net.Conn){
				err := conn.Close()
				if err != nil {
					println(fmt.Sprintf("<%T>: %s", err, err))
				}
			}(s.repeatTo[response.idx])
			// Despite the fact that the array element is not deleted and this is officially a memory leak,
			// I do not consider this critical
			s.repeatTo[response.idx] = nil
			oBody = []byte(fmt.Sprintf("<%T>: %s", response.err, response.err))
		} else
		// the higher the response status, the higher the response priority
		if oStatus < response.response.StatusCode {
			oStatus = response.response.StatusCode
			oHeader = response.response.Header
			if response.response.Body != nil {
				oBody, response.err = ioutil.ReadAll(response.response.Body)
				// I can not imagine the reasons for which this could happen, but the handler wrote here
				if response.err != nil && response.err != io.EOF {
					return http.StatusInternalServerError, nil, []byte(fmt.Sprintf("<%T>: %s", response.err, response.err))
				}
			} else {
				oBody = nil
			}
		}
	}
	return
}

// This is the starting function for receiving the HTTP-request.
func (s *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request){
	bodyBytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		println(fmt.Sprintf("<%T>: %s", err, err))
		bodyBytes = []byte{}
	}
	println(fmt.Sprintf(
		"%s\nREQUEST FROM %s TO %s\n%s\n%s %s %s",
		trailLine,
		r.RemoteAddr,
		r.Host,
		trailLine,
		r.Method,
		r.URL.String(),
		r.Proto,
	))
	for k, v := range r.Header {
		println(fmt.Sprintf("%s: %s", k, strings.Join(v, "\n")))
	}
	if len(bodyBytes) > 0 {
		println("BODY:")
		println(string(bodyBytes))
	}
	if *repeat != "" {
		// Repeat request for all connected sockets
		println(fmt.Sprintf("%s\nRESPONSE:\n%s\n%s", trailLine, *hello, trailLine))
		status, headers, body := s.RepeatToSocket(r, bodyBytes)
		if status == 0 {
			// This is done if errors come from all the sockets.
			w.WriteHeader(http.StatusOK)
			if body != nil && len(body) > 0 {
				w.Write(body)
			} else {
				w.Write([]byte(*hello))
			}
			return
		}
		// All received data will be accurately returned as an HTTP-response.
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
		// This answer is a dummy
		println(fmt.Sprintf("%s\nRESPONSE:\n%s\n%s", trailLine, *hello, trailLine))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(*hello))
	}
}