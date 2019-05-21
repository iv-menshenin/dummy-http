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
	"os"
)

type(
	httpRepeaterSocket struct {
		mux sync.Mutex
		connection net.Conn
	}
	httpHandler struct {
		mux sync.Mutex
		stopper chan interface{}
		listener net.Listener
		haveRepeater bool
		repeatTo []httpRepeaterSocket
		helloStr string
	}
)

var(
	trailLine  = strings.Repeat("-", 80)
	extraLine  = strings.Repeat("*", 80)
)

const intro = "The dummy listener for HTTP ports allows you to forward requests to remote servers or local stations using socket-repeaters. It works in two modes:\n    1. Read and repeat.\n    2. Receiving and processing a request"

func main(){
	var(
		addr      *string
		hello     *string
		repeat    *string
		repeater  *string
		sendTo    *string
	)
	flag.Usage = func(){
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n%s\n%s", trailLine, intro, trailLine)
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}
	addr = flag.String("addr", "", "Specify address (host:port) for listening. Turns on mode 1")
	hello = flag.String("hello", "Hello world!", "Specify the default response data string")
	repeat = flag.String("repeat", "", "Specify the bind address (IP:port) to create a socket-repeater. For mode 1 only")
	repeater = flag.String("repeater", "", "Specify the address (host:port) of the remote socket repeater to receive requests from him. Turns on 2")
	sendTo = flag.String("send_to", "", "Specify the URL of the HTTP server (eg. http://localhost:8800) that will process the requests and give the final result.")
	flag.Parse()
	if *repeater != "" && *addr != "" {
		panic(errors.New("you cannot specify both addr and repeater, because these are source"))
	}
	if *repeater != "" && *repeat != "" {
		panic(errors.New("you cannot specify both repeat and repeater"))
	}
	if *repeater == "" && *addr == "" {
		flag.Usage()
		return
	}
	var handler httpHandler
	var w sync.WaitGroup
	if *repeater == "" {
		handler.stopper = make(chan interface{})
		handler.helloStr = *hello
		if *repeat != "" {
			w.Add(1)
			handler.haveRepeater = true
			handler.initRepeaterService(*repeat, func(){
				w.Done()
			})
		}
		println(fmt.Sprintf("Listen at %s. Returning data: %s", *addr, *hello))
		err := http.ListenAndServe(*addr, &handler)
		if err != nil {
			panic(err)
		}
	} else {
		var handlerURL *url.URL
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
				return handler.initSocketReaderAndProcessRequests(handlerURL, *repeater, *hello)
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
func (s *httpHandler) initRepeaterService(repeat string, onDone func()){
	println(fmt.Sprintf("Start listener on: %s (Socket-Repeater)", repeat))
	var err error
	var ch = make(chan net.Conn)
	if s.listener, err = net.Listen("tcp", repeat); err != nil {
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
					s.repeatTo = append(s.repeatTo, httpRepeaterSocket{connection: conn})
				}()
			case <-s.stopper: return
			case <-time.After(time.Second * 10):
				s.pingAll()
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
		handlerURL *url.URL         // address of the server that will process the request
		connection net.Conn         // socket connection
		reader     *bufio.Reader    // same socket but just reader interface
		body       []byte           // the copy of the original request body
		idx        int              // index of net.Conn element
		mux        *sync.Mutex
	}
)

// implements the transfer of a single command to the socket, as well as parsing and checking the correctness of the answer
func (t *customHTTPTransport) sendCommand(cmd, expect string) (response string, attribute int64) {
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
func (t *customHTTPTransport) receiveCommand(expect, response string) (cmd string, attribute int64) {
	var(
		request string
		err error
	)
	for{
		request, err = t.reader.ReadString('?')
		if err != nil {
			panic(err)
		}
		if request == "PING?" {
			_, err = t.connection.Write([]byte("OK!"))
			continue
		}
		break
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
func getAndProcessRequestFromNetwork(dataLen int64, transport *customHTTPTransport, helloStr string) (request *http.Request, response *http.Response, err error){
	var buf bytes.Buffer
	if err := readFromNetwork(dataLen, transport.reader, &buf); err != nil {
		panic(err)
	}
	println(extraLine)
	println(string(buf.Bytes()))
	println(extraLine)
	request, err = http.ReadRequest(bufio.NewReader(bytes.NewReader(buf.Bytes())))
	if err != nil {
		return
	}
	if transport.handlerURL != nil {
		request.URL = transport.handlerURL
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
		response.Body = ioutil.NopCloser(bytes.NewReader([]byte(helloStr)))
		response.StatusCode = 200
		response.Status = "200 OK"
		response.Proto = "HTTP/1.0"
		response.Request = request
		response.ContentLength = int64(len([]byte(helloStr)))
		response.Header = map[string][]string{
			"X-Request-Status": {"HELLO"},
			"X-Request-Origin": {"DUMMY"},
		}
	}
	return
}

// Receives one request from the service socket and executes it, giving the result back to the socket
func getAndProcessRemoteRequest(transport customHTTPTransport, helloStr string) {
	var responseData bytes.Buffer
	transport.receiveCommand("READY", "READY")
	_, dataLen := transport.receiveCommand("RECEIVE", "YES")
	request, response, err := getAndProcessRequestFromNetwork(dataLen, &transport, helloStr)
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
	transport.receiveCommand("RETRIEVE", fmt.Sprintf("CATCH %d", responseData.Len()))
	_, err = transport.connection.Write(responseData.Bytes())
	println(extraLine)
	println(string(responseData.Bytes()))
	println(extraLine)
	if err != nil {
		panic(err)
	}
	transport.receiveCommand("BYE", "BYE")
}

// Create a connection to the service socket-repeater for receiving HTTP-requests from it.
func (s *httpHandler) initSocketReaderAndProcessRequests(handlerURL *url.URL, repeater, helloStr string) (breakProcess bool) {
	conn, err := net.Dial("tcp", repeater)
	if err != nil {
		println(fmt.Sprintf("Critical error when connecting attempt: <%T>: %s", err, err))
		return true
	}
	transport := customHTTPTransport{
		handlerURL: handlerURL,
		connection: conn,
		reader: bufio.NewReader(conn),
	}
	for {
		getAndProcessRemoteRequest(transport, helloStr)
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

	t.sendCommand("READY", "READY")
	t.sendCommand(fmt.Sprintf("RECEIVE %d", httpRequestRawData.Len()), "YES")

	_, err = httpRequestRawData.WriteTo(t.connection)
	if err != nil {
		return nil, err
	}
	var httpResponseRawData bytes.Buffer
	_, contentLen := t.sendCommand("RETRIEVE", "CATCH")
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
	t.sendCommand("BYE", "BYE")

	return http.ReadResponse(bufio.NewReader(bytes.NewReader(httpResponseRawData.Bytes())), r)
}

// Implements pinging for all unused connections. Removes link to connection if it is broken.
func (s *httpHandler) pingAll() {
	// links to connections are obtained under the server mutex
	repeaters := func() []*httpRepeaterSocket {
		s.mux.Lock()
		defer s.mux.Unlock()
		result := make([]*httpRepeaterSocket, 0, len(s.repeatTo))
		for i := range s.repeatTo {
			if s.repeatTo[i].connection != nil {
				result = append(result, &s.repeatTo[i])
			}
		}
		return result
	}()

	var w sync.WaitGroup
	w.Add(len(repeaters))
	for i := range repeaters {
		go func(repeater *httpRepeaterSocket){
			defer w.Done()
			repeater.mux.Lock()
			defer repeater.mux.Unlock()
			defer func(){
				if recover() != nil {
					repeater.connection = nil
				}
			}()
			if repeater.connection == nil {
				return
			}
			// as a ping message we send just a PING, and as a response we expect OK
			// the remote socket can process this message correctly only when it is waiting for the "READY" command,
			// that is, between two transactions
			var test = []byte{0, 0, 0}
			_, err := repeater.connection.Write([]byte("PING?"))
			if err != nil {
				panic(err)
			}
			_, err = repeater.connection.Read(test)
			if err != nil {
				panic(err)
			}
			if string(test) != "OK!" {
				panic("broken")
			}
		}(repeaters[i])
	}
	w.Wait()
}

// Marking the connection as closed. We simply clear the link to the net.Conn
func (s *httpHandler) markConnectionAsClosed(idx int) {
	s.mux.Lock()
	if !(len(s.repeatTo) > idx) {
		s.mux.Unlock()
		return
	}
	repeater := &s.repeatTo[idx]
	s.mux.Unlock()
	repeater.mux.Lock()
	defer repeater.mux.Unlock()
	if repeater.connection != nil {
		if err := repeater.connection.Close(); err != nil {
			println(fmt.Sprintf("<%T>: %s", err, err))
		}
		// Despite the fact that the array element is not deleted and this is officially a memory leak,
		// I do not consider this critical
		repeater.connection = nil
	}
}

// Build an http.Client array from a socket-repeaters array. Running under server mutex
func (s *httpHandler) getCustomHTTPClients(reqBody []byte) []http.Client {
	s.mux.Lock()
	defer s.mux.Unlock()
	var clients = make([]http.Client, 0, len(s.repeatTo))
	for i := range s.repeatTo {
		if s.repeatTo[i].connection != nil {
			clients = append(
				clients,
				http.Client{
					Transport: &customHTTPTransport{
						mux: &s.repeatTo[i].mux,
						connection: s.repeatTo[i].connection,
						reader: bufio.NewReader(s.repeatTo[i].connection),
						body: reqBody,
						idx: i,
					},
				},
			)
		}
	}
	return clients
}

// The request will be repeated for all connections, all responses will be collected and analyzed.
// One result will be selected as the resulting answer.
func (s *httpHandler) repeatToSocket(r *http.Request, body []byte) (oStatus int, oHeader http.Header, oBody []byte){
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
		httpClients = s.getCustomHTTPClients(body)
	)
	r.RequestURI = ""
	w.Add(len(httpClients))
	for _, client := range httpClients {
		// Warning!
		// Any of these connections can be closed at any time, so we must mark such connections as complete.
		// For this purpose, we transmit error information to the channel and as soon as it is read, the link
		// to the connection will be set to nil. Such compounds will no longer be used.
		go func(client http.Client, reqCopy *http.Request){
			client.Transport.(*customHTTPTransport).mux.Lock()
			defer client.Transport.(*customHTTPTransport).mux.Unlock()
			defer w.Done()
			respFromSocket, errFromSocket := client.Do(reqCopy)
			ch <- responseElem{
				response: respFromSocket,
				err:      errFromSocket,
				idx:      client.Transport.(*customHTTPTransport).idx,
			}
		}(client, r)
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
			s.markConnectionAsClosed(response.idx)
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
	if s.haveRepeater {
		// Repeat request for all connected sockets
		println(fmt.Sprintf("%s\nRESPONSE:\n%s\n%s", trailLine, s.helloStr, trailLine))
		status, headers, body := s.repeatToSocket(r, bodyBytes)
		if status == 0 {
			// This is done if errors come from all the sockets.
			w.WriteHeader(http.StatusOK)
			if body != nil && len(body) > 0 {
				w.Write(body)
			} else {
				w.Write([]byte(s.helloStr))
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
		println(fmt.Sprintf("%s\nRESPONSE:\n%s\n%s", trailLine, s.helloStr, trailLine))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(s.helloStr))
	}
}