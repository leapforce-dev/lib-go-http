package http

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	errortools "github.com/leapforce-libraries/go_errortools"
	ig "github.com/leapforce-libraries/go_integration"
	utilities "github.com/leapforce-libraries/go_utilities"
)

const defaultMaxRetries uint = 5

type Accept string

const (
	AcceptJSON Accept = "json"
	AcceptXML  Accept = "xml"
)

type Service struct {
	accept       Accept
	client       http.Client
	requestCount int64
}

type ServiceConfig struct {
	Accept     *Accept
	HTTPClient *http.Client
}

func NewService(serviceConfig *ServiceConfig) (*Service, *errortools.Error) {
	if serviceConfig == nil {
		return nil, errortools.ErrorMessage("ServiceConfig must not be a nil pointer")
	}

	accept := AcceptJSON
	httpClient := http.Client{}

	if serviceConfig != nil {
		if serviceConfig.Accept != nil {
			accept = *serviceConfig.Accept
		}
		if serviceConfig.HTTPClient != nil {
			httpClient = *serviceConfig.HTTPClient
		}
	}

	return &Service{
		accept: accept,
		client: httpClient,
	}, nil
}

type RequestConfig struct {
	URL                string
	Parameters         *url.Values
	BodyModel          interface{}
	ResponseModel      interface{}
	ErrorModel         interface{}
	NonDefaultHeaders  *http.Header
	XWWWFormURLEncoded *bool
	MaxRetries         *uint
}

func (requestConfig *RequestConfig) FullURL() string {
	if requestConfig.Parameters == nil {
		return requestConfig.URL
	}

	return fmt.Sprintf("%s?%s", requestConfig.URL, requestConfig.Parameters.Encode())
}

func (requestConfig *RequestConfig) SetParameter(key string, value string) {
	if requestConfig.Parameters == nil {
		requestConfig.Parameters = &url.Values{}
	}

	requestConfig.Parameters.Set(key, value)
}

func (service *Service) HTTPRequest(httpMethod string, requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	e := new(errortools.Error)

	if ig.Debug() {
		fmt.Printf("DEBUG - FullURL\n%s\n", requestConfig.FullURL())
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

	request, err := func() (*http.Request, error) {
		if utilities.IsNil(requestConfig.BodyModel) {
			return http.NewRequest(httpMethod, requestConfig.FullURL(), nil)
		}

		if requestConfig.XWWWFormURLEncoded != nil {
			if *requestConfig.XWWWFormURLEncoded {
				tag := "json"
				url, e := utilities.StructToURL(&requestConfig.BodyModel, &tag)
				if e != nil {
					return nil, errors.New(e.Message())
				}

				return http.NewRequest(httpMethod, requestConfig.FullURL(), strings.NewReader(*url))
			}
		}

		var b []byte
		var err error

		if service.accept == AcceptXML {
			b, err = xml.Marshal(requestConfig.BodyModel)
		} else {
			b, err = json.Marshal(requestConfig.BodyModel)
		}
		if err != nil {
			return nil, err
		}

		if ig.Debug() {
			fmt.Printf("DEBUG - BodyModel\n%s\n", string(b))
			fmt.Println("------------------------")
		}

		return http.NewRequest(httpMethod, requestConfig.FullURL(), bytes.NewBuffer(b))

	}()

	e.SetRequest(request)

	if err != nil {
		e.SetMessage(err)
		return request, nil, e
	}

	// default headers
	if service.accept == AcceptJSON {
		request.Header.Set("Accept", "application/json")
		if !utilities.IsNil(requestConfig.BodyModel) {
			request.Header.Set("Content-Type", "application/json")
		}
	}

	// overrule with input headers
	if requestConfig.NonDefaultHeaders != nil {
		for key, values := range *requestConfig.NonDefaultHeaders {
			request.Header.Del(key) //delete old header
			for _, value := range values {
				request.Header.Add(key, value) //add new header(s)
			}
		}
	}

	// Send out the HTTP request
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
		return request, nil, e
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
			e.SetRequest(request)
			e.SetResponse(response)

			if !utilities.IsNil(requestConfig.ErrorModel) {
				b, ee := responseBodyToBytes(response)
				if ee != nil {
					errortools.CaptureError(ee)
				} else {
					// try to unmarshal to ErrorModel
					var errError error
					if service.accept == AcceptXML {
						errError = xml.Unmarshal(*b, &requestConfig.ErrorModel)
					} else {
						errError = json.Unmarshal(*b, &requestConfig.ErrorModel)
					}
					if errError != nil {
						errortools.CaptureError(errError)
						e.SetExtra("response_message", string(*b))
					}
				}
			}

			return request, response, e
		}

		if !utilities.IsNil(requestConfig.ResponseModel) {
			b, ee := responseBodyToBytes(response)
			if ee != nil {
				return request, response, ee
			}

			if service.accept == AcceptXML {
				err = xml.Unmarshal(*b, &requestConfig.ResponseModel)
			} else {
				err = json.Unmarshal(*b, &requestConfig.ResponseModel)
			}
			if err != nil {
				if e == nil {
					e = new(errortools.Error)
				}
				e.SetRequest(request)
				e.SetResponse(response)
				e.SetMessage(err)

				return request, response, e
			}
		}
	}

	return request, response, nil
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

func (service *Service) get(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.HTTPRequest(http.MethodGet, requestConfig)
}

func (service *Service) post(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.HTTPRequest(http.MethodPost, requestConfig)
}

func (service *Service) put(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.HTTPRequest(http.MethodPut, requestConfig)
}

func (service *Service) delete(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.HTTPRequest(http.MethodDelete, requestConfig)
}

func (service *Service) RequestCount() int64 {
	return service.requestCount
}

func (service *Service) ResetRequestCount() {
	service.requestCount = 0
}

// doWithRetry executes http.Request and retries in case of 500 range status code
// see: https://developers.google.com/analytics/devguides/config/mgmt/v3/errors#handling_500_or_503_responses
func (service *Service) doWithRetry(client *http.Client, request *http.Request, maxRetries *uint) (*http.Response, *errortools.Error) {
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

	for retry <= _maxRetries {
		if retry > 0 {
			fmt.Printf("Starting retry %v for %s %s\n", retry, request.Method, request.URL.String())
			waitSeconds := math.Pow(2, float64(retry-1))
			waitMilliseconds := int(rand.Float64() * 1000)
			time.Sleep(time.Duration(waitSeconds)*time.Second + time.Duration(waitMilliseconds)*time.Millisecond)
		}

		response, err := client.Do(request)
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

		statusCode := 0
		if response != nil {
			statusCode = response.StatusCode
		}

		if (statusCode == 500 || statusCode == 503) && retry < _maxRetries { // retry in case of status 500/503 (server error)
			retry++
		} else {
			if err == nil && (statusCode/100 == 4 || statusCode/100 == 5) {
				err = fmt.Errorf("Server returned statuscode %v", statusCode)
			}

			if err != nil {
				e := new(errortools.Error)
				e.SetRequest(request)
				e.SetResponse(response)
				e.SetMessage(err.Error())

				return response, e
			}

			return response, nil
		}
	}

	// should never reach this
	return nil, nil
}
