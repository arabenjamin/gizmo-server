package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/hybridgroup/mjpeg"
)

type PingResonse struct {
	Message string `json:"message"`
}

// Channel to hold the video stream
var videoStream chan []byte

var streamBuffer []byte

var Stream *mjpeg.Stream

// CustomBufferReader reads from a byte buffer
type CustomBufferReader struct {
	buf *bytes.Buffer
}

func (cbr *CustomBufferReader) Read(p []byte) (n int, err error) {
	return cbr.buf.Read(p)
}

/* Boilerplate */
func ping(res http.ResponseWriter, req *http.Request) {

	if req.Method != http.MethodGet {
		http.Error(res, "Invalid request method", http.StatusMethodNotAllowed)
	}
	payload := map[string]interface{}{
		"message": "pong!",
	}

	json_resp, _ := json.Marshal(payload)
	res.Header().Set("Content-Type", "application/json")
	res.Header().Set("Access-Control-Allow-Origin", "*")
	res.WriteHeader(http.StatusOK)
	res.Write(json_resp)
}

// Endpoint to receive video from the device
func upload(resp http.ResponseWriter, req *http.Request) {

	//log.Println("Recieving upload from device ")

	if req.Method != http.MethodPost {
		http.Error(resp, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Read the video data from the request body and send it to the channel
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(resp, "Error reading request body", http.StatusBadRequest)
		return
	}
	defer req.Body.Close()
	streamBuffer = body

	Stream = mjpeg.NewStream()
	Stream.UpdateJPEG(streamBuffer)
	//log.Printf("Received POST request with body: %s\n", string(body))

	resp.WriteHeader(http.StatusOK)
	resp.Write([]byte("POST request received successfully"))

}

func stream(resp http.ResponseWriter, req *http.Request) {

	log.Println("Starting Stream to client .... ")
	//resp.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame") // Adjust content type as needed

	// Write the frame to the HTTP response

	// Continuously read from the channel and write to the response

	for {

		//log.Printf("Sending %d bytes to client", len(streamBuffer))
		resp.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")

		/*
			fmt.Fprintf(resp, "--frame\r\n")
			fmt.Fprintf(resp, "Content-Type: image/jpeg\r\n")
			fmt.Fprintf(resp, "Content-Length: %d\r\n\r\n", len(streamBuffer))
			//resp.Write(streamBuffer)
			fmt.Fprintf(resp, "\r\n")

			Stream.ServeHTTP(resp, req)
		*/
		bufferReader := &CustomBufferReader{buf: bytes.NewBuffer(streamBuffer)}

		// Set appropriate headers for video streaming
		resp.Header().Set("Content-Type", "video/mp4") // "image/jpeg" Adjust content type as needed
		resp.Header().Set("Transfer-Encoding", "chunked")
		// Stream the buffer data
		_, err := io.Copy(resp, bufferReader)
		if err != nil {
			http.Error(resp, "Error streaming video", http.StatusInternalServerError)
			return
		}

		/*
			if f, ok := resp.(http.Flusher); ok {
				f.Flush()
			}
		*/
		/*
			data, ok := <-videoStream
			if !ok {

				break
			}
			log.Printf("Data from videostream channel %v", data)
		*/

		/*
			for data := range videoStream {

				log.Printf("Sending %d bytes to client", len(data))

				fmt.Fprintf(resp, "--frame\r\n")
				fmt.Fprintf(resp, "Content-Type: image/jpeg\r\n")
				fmt.Fprintf(resp, "Content-Length: %d\r\n\r\n", len(data))
				//resp.Write(data)

				_, err := resp.Write(data)
				if err != nil {
					log.Println("Error writing to response:", err)
					return
				}
				fmt.Fprintf(resp, "\r\n")
				if f, ok := resp.(http.Flusher); ok {
					f.Flush()
				}

			}
		*/
	}

}
