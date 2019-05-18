# dummy-http
Dummy HTTP server for development checks

## intro
Sometimes in the process of programming, we have to debug our program’s interaction with a third-party service, for example, when developing web-based hooks.
Such tasks are complicated by the security policy of remote services, the complexity of running programs in debug mode on a remote server, and other network and infrastructure nuances.

Sometimes it would be so desirable that the remote service, not noticing the substitution, continued to send requests to our HTTP server, and these requests through some kind of tunnel (or magic) came to our local computer, where we could process them and respond.
Then we could, step by step, go through all the stages of processing the request directly during the debugging process, monitor the state of variables and other important details.

__Now it can be done in just three steps!!!__

## step one `build`
No matter how strange it is, first copy the repository and build the project from the source code.
```sh
go get -u github.com/iv-menshenin/dummy-http
go build github.com/iv-menshenin/dummy-http
```
the deploy binary `dummy-http` onto your server (e.g. someapp.someserve.com)

## step two `dummy`
To open the network port, run the utility with the parameters
  - `addr` - port to listen
  - `hello` - text string to be transmitted in response to any requests
  - `repeat` - the special port-repeater
```sh
./dummy-http --addr :8040 --hello 'Hello world!' --repeat :16999
```
and then any http request executed on this port will receive in response `hello` line

## step three `retranslate`
To transfer the request to another server (local station), run it on the second server with the following parameters:
```sh
./dummy-http --send_to http://localhost:8060 --repeater someapp.someserve.com:16999
```
it is assumed that we have a debugging process running on port 8060 on our local machine

here is
  - `sent_to` - host address and the port to which the request is repeated
  - `repeater` - full address (host and port) on which the special port-repeater was started

now all requests to someapp.someserve.com will be repeated on port 8060 of your local machine, and the result will be transmitted back as a HTTP response