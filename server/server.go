package server


import (
  
  "log"
  "net/http"

)

type Middleware func(http.HandleFunc) http.HandleFunc

func logger(serverlog *log.Logger) Middleware {
    return func(next http.HandlerFunc) http.HandlerFunc {
       return func(resp http.Response, req *http.Request){
           defer func(){
              serverlog.Printf("[%v] [%v] [%v %v] %v\n", req.RemoteAddr, req.Method, req.Proto, req.URL.Path, req.Header["User-Agent"])
           }()
           next(resp, req)
       }
    }
}

func Chain(f http.HandlerFunc, middlewares ...Middleware) http.HandlerFunc {

    for _, middleware := range middlewares {
        f = middleware(f)
    }
    return f
}

func Start(serverlog *log.Logger) error{

    mux := http.NewServeMux()
    mux.HandleFunc("/api/v1/ping", Chain(ping, logger(serverlog))

    err := http.ListenAndServer(":9090", mux)
    if err != nil {

        return err
    }

}

