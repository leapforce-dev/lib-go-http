package http

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	errortools "github.com/leapforce-libraries/go_errortools"
	ig "github.com/leapforce-libraries/go_integration"
	utilities "github.com/leapforce-libraries/go_utilities"
	"io"
	"io/ioutil"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultMaxRetries uint = 5

type Accept string

const (
	AcceptJson Accept = "json"
	AcceptXml  Accept = "xml"
	AcceptRaw  Accept = "raw"
)

type Service struct {
	accept       Accept
	client       http.Client
	requestCount int64
}

type ServiceConfig struct {
	Accept     *Accept
	HttpClient *http.Client
	ProxyUrl   *string
}

func NewService(serviceConfig *ServiceConfig) (*Service, *errortools.Error) {
	accept := AcceptJson
	httpClient := http.Client{}

	if serviceConfig != nil {
		if serviceConfig.Accept != nil {
			accept = *serviceConfig.Accept
		}
		if serviceConfig.HttpClient != nil {
			httpClient = *serviceConfig.HttpClient
		}

		if serviceConfig.ProxyUrl != nil {
			proxyUrl, err := url.Parse(*serviceConfig.ProxyUrl)
			if err != nil {
				return nil, errortools.ErrorMessage(err)
			}
			httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(proxyUrl)}
		}
	}

	return &Service{
		accept: accept,
		client: httpClient,
	}, nil
}

type RequestConfig struct {
	Method             string // not used yet
	RelativeUrl        string
	Url                string
	Parameters         *url.Values
	BodyModel          interface{}
	BodyRaw            *[]byte
	ResponseModel      interface{}
	ErrorModel         interface{}
	NonDefaultHeaders  *http.Header
	XWwwFormUrlEncoded *bool
	MaxRetries         *uint
}

func (requestConfig *RequestConfig) FullUrl() string {
	if requestConfig.Parameters == nil {
		return requestConfig.Url
	}

	return fmt.Sprintf("%s?%s", requestConfig.Url, requestConfig.Parameters.Encode())
}

func (requestConfig *RequestConfig) SetParameter(key string, value string) {
	if requestConfig.Parameters == nil {
		requestConfig.Parameters = &url.Values{}
	}

	requestConfig.Parameters.Set(key, value)
}

func (service *Service) HttpRequest(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	e := new(errortools.Error)

	if ig.Debug() {
		fmt.Printf("DEBUG - FullUrl\n%s\n", requestConfig.FullUrl())
		fmt.Println("------------------------")
		if !utilities.IsNil(requestConfig.ResponseModel) {
			fmt.Printf("DEBUG - ResponseModel\n%T\n", requestConfig.ResponseModel)
			fmt.Println("------------------------")
		}
		if !utilities.IsNil(requestConfig.ErrorModel) {
			fmt.Printf("DEBUG - ErrorModel\n%T\n", requestConfig.ErrorModel)
			fmt.Println("------------------------")
		}
	}

	request, err := func() (*RetryableRequest, error) {
		var body []byte
		var err error

		if requestConfig.BodyRaw != nil {
			body = *requestConfig.BodyRaw
		} else if utilities.IsNil(requestConfig.BodyModel) {
			return NewRetryableRequest(requestConfig.Method, requestConfig.FullUrl(), nil)
		} else if service.accept == AcceptXml {
			body, err = xml.Marshal(requestConfig.BodyModel)
		} else {
			body, err = json.Marshal(requestConfig.BodyModel)
		}
		if err != nil {
			return nil, err
		}

		if requestConfig.XWwwFormUrlEncoded != nil {
			if *requestConfig.XWwwFormUrlEncoded {
				tag := "json"
				url, e := utilities.StructToUrl(&requestConfig.BodyModel, &tag)
				if e != nil {
					return nil, errors.New(e.Message())
				}

				return NewRetryableRequest(requestConfig.Method, requestConfig.FullUrl(), strings.NewReader(*url))
			}
		}

		if ig.Debug() {
			if requestConfig.BodyRaw != nil {
				fmt.Printf("DEBUG - BodyRaw\nlength = %v, %v\n", len(*requestConfig.BodyRaw), len(body))
				fmt.Println("------------------------")
			} else if !utilities.IsNil(requestConfig.BodyModel) {
				fmt.Printf("DEBUG - BodyModel\n%s\n", string(body))
				fmt.Println("------------------------")
			}
		}

		return NewRetryableRequest(requestConfig.Method, requestConfig.FullUrl(), bytes.NewReader(body))
	}()
	if err != nil {
		e.SetMessage(err)
		return nil, nil, e
	}

	e.SetRequest(request.Request)
	e.SetBody(request.body)

	// default headers
	if service.accept == AcceptJson {
		request.Header.Set("Accept", "application/json")
		if !utilities.IsNil(requestConfig.BodyModel) {
			request.Header.Set("Content-Type", "application/json")
		}
	}

	// overrule with input headers
	if requestConfig.NonDefaultHeaders != nil {
		if ig.Debug() {
			fmt.Println("DEBUG - NonDefaultHeaders")
		}
		for key, values := range *requestConfig.NonDefaultHeaders {
			request.Header.Del(key) //delete old header
			for _, value := range values {
				request.Header.Add(key, value) //add new header(s)
				if ig.Debug() {
					fmt.Printf("%s : %s\n", key, value)
				}
			}
		}
		if ig.Debug() {
			fmt.Println("------------------------")
		}
	}

	// Send out the Http request
	errortools.SetContext("http_url", request.URL)
	defer errortools.RemoveContext("http_url")

	service.requestCount++

	if ig.Debug() {
		fmt.Printf("DEBUG - Request\n%v\n", request)
		fmt.Println("------------------------")
		fmt.Printf("DEBUG - Client\n%v\n", service.client)
		fmt.Println("------------------------")
	}

	response, e := service.doWithRetry(&service.client, request, requestConfig.MaxRetries)

	if ig.Debug() {
		fmt.Printf("DEBUG - Response\n%v\n", response)
		fmt.Println("------------------------")
	}

	if response == nil {
		return request.Request, nil, e
	}

	if response != nil {
		if ig.Debug() {
			fmt.Printf("DEBUG - StatusCode\n%v\n", response.StatusCode)
			fmt.Println("------------------------")
		}

		if e == nil {
			if response.StatusCode < 200 || response.StatusCode > 299 {
				e = new(errortools.Error)
				e.SetMessage(fmt.Sprintf("Server returned statuscode %v", response.StatusCode))
			}
		}

		if e != nil {
			e.SetRequest(request.Request)
			e.SetBody(request.body)
			e.SetResponse(response)

			if !utilities.IsNil(requestConfig.ErrorModel) {
				// try to unmarshal to ErrorModel
				b, errToBytes := responseBodyToBytes(response)
				if errToBytes == nil {
					var err2 error
					if service.accept == AcceptXml {
						err2 = xml.Unmarshal(*b, &requestConfig.ErrorModel)
					} else {
						err2 = json.Unmarshal(*b, &requestConfig.ErrorModel)
					}
					if err2 != nil {
						e.SetExtra("response_message", string(*b))
					}
				}
			}

			return request.Request, response, e
		}

		if !utilities.IsNil(requestConfig.ResponseModel) {
			// try to unmarshal to ResponseModel
			b, errToBytes := responseBodyToBytes(response)
			if errToBytes != nil {
				return request.Request, response, errToBytes
			}

			if service.accept == AcceptXml {
				err = xml.Unmarshal(*b, &requestConfig.ResponseModel)
			} else {
				err = json.Unmarshal(*b, &requestConfig.ResponseModel)
			}
			if err != nil {
				if e == nil {
					e = new(errortools.Error)
				}
				e.SetRequest(request.Request)
				e.SetBody(request.body)
				e.SetResponse(response)
				e.SetMessage(err)

				return request.Request, response, e
			}
		}
	}

	return request.Request, response, nil
}

func responseBodyToBytes(response *http.Response) (*[]byte, *errortools.Error) {
	if response == nil {
		return nil, nil
	}

	if response.Body == nil {
		fmt.Println("DEBUG - ResponseBody is nil")
		fmt.Println("------------------------")
		return nil, nil
	}
	defer response.Body.Close()

	b, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, errortools.ErrorMessage(err)
	}

	if ig.Debug() {
		fmt.Printf("DEBUG - ResponseBody\n%s\n", string(b))
		fmt.Println("------------------------")
	}

	return &b, nil
}

func (service *Service) RequestCount() int64 {
	return service.requestCount
}

func (service *Service) ResetRequestCount() {
	service.requestCount = 0
}

type RetryableRequest struct {
	body     []byte
	runCount int
	*http.Request
}

func NewRetryableRequest(method, url string, reader io.Reader) (*RetryableRequest, error) {
	var body []byte = nil

	if reader != nil {
		b, err := ioutil.ReadAll(reader)
		if err != nil {
			return nil, err
		}

		body = b
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	return &RetryableRequest{body, 0, req}, nil
}

func (r *RetryableRequest) Do(client *http.Client) (*http.Response, error) {
	if r.runCount > 0 && r.body != nil {
		reader := bytes.NewReader(r.body)
		readCloser := io.NopCloser(reader)
		r.Request.Body = readCloser
	}
	r.runCount++

	return client.Do(r.Request)
}

// doWithRetry executes http.Request and retries in case of 500 range status code
// see: https://developers.google.com/analytics/devguides/config/mgmt/v3/errors#handling_500_or_503_responses
func (service *Service) doWithRetry(client *http.Client, request *RetryableRequest, maxRetries *uint) (*http.Response, *errortools.Error) {
	if client == nil || request == nil {
		if ig.Debug() {
			if client == nil {
				fmt.Println("DEBUG - client is nil")
				fmt.Println("------------------------")
			}
			if request == nil {
				fmt.Println("DEBUG - request is nil")
				fmt.Println("------------------------")
			}
		}
		return nil, nil
	}

	retry := uint(0)
	_maxRetries := defaultMaxRetries
	if maxRetries != nil {
		_maxRetries = *maxRetries
	}

	statusCode := 0

	for retry <= _maxRetries {
		if retry > 0 {
			fmt.Printf("StatusCode: %v, starting retry %v for %s %s\n", statusCode, retry, request.Method, request.URL.String())
			waitSeconds := math.Pow(2, float64(retry-1))
			waitMilliseconds := int(rand.Float64() * 1000)
			time.Sleep(time.Duration(waitSeconds)*time.Second + time.Duration(waitMilliseconds)*time.Millisecond)
		}

		response, err := request.Do(client)
		//response, err := client.Do(request.Request)
		if ig.Debug() {
			if err != nil {
				fmt.Printf("DEBUG - client.Do - error\n%s\n", err.Error())
				fmt.Println("------------------------")
			}
			if response == nil {
				fmt.Println("DEBUG - client.Do - response is nil")
				fmt.Println("------------------------")
			}
		}

		if response != nil {
			statusCode = response.StatusCode
		} else {
			statusCode = 0
		}

		if ig.HttpRetry(statusCode) && retry < _maxRetries {
			retry++
			continue
		}

		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "tls handshake timeout") {
				retry++
				continue
			}
		}

		if err == nil && (statusCode/100 == 4 || statusCode/100 == 5) {
			err = fmt.Errorf("server returned statuscode %v", statusCode)
		}

		if err != nil {
			e := new(errortools.Error)

			// make body re-readable for error logging
			e.SetRequest(request.Request)
			e.SetBody(request.body)
			e.SetResponse(response)
			e.SetMessage(err.Error())

			return response, e
		}

		return response, nil
	}

	// should never reach this
	return nil, nil
}
