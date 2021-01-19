package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"

	errortools "github.com/leapforce-libraries/go_errortools"
	utilities "github.com/leapforce-libraries/go_utilities"
)

const (
	DefaultMaxRetries            uint   = 0
	DefaultSecondsBetweenRetries uint32 = 3
)

type Service struct {
	client                http.Client
	maxRetries            uint
	secondsBetweenRetries uint32
}

type ServiceConfig struct {
	MaxRetries            *uint
	SecondsBetweenRetries *uint32
}

func NewService(requestConfig ServiceConfig) *Service {
	maxRetries := DefaultMaxRetries

	if requestConfig.MaxRetries != nil {
		maxRetries = *requestConfig.MaxRetries
	}

	secondsBetweenRetries := DefaultSecondsBetweenRetries

	if requestConfig.SecondsBetweenRetries != nil {
		secondsBetweenRetries = *requestConfig.SecondsBetweenRetries
	}

	return &Service{
		client:                http.Client{},
		maxRetries:            maxRetries,
		secondsBetweenRetries: secondsBetweenRetries,
	}
}

type RequestConfig struct {
	URL               string
	BodyModel         interface{}
	ResponseModel     interface{}
	ErrorModel        interface{}
	NonDefaultHeaders *http.Header
}

func (service *Service) httpRequest(httpMethod string, requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	e := new(errortools.Error)

	request, err := func() (*http.Request, error) {
		if utilities.IsNil(requestConfig.BodyModel) {
			return http.NewRequest(httpMethod, requestConfig.URL, nil)
		} else {
			b, err := json.Marshal(requestConfig.BodyModel)
			if err != nil {
				return nil, err
			}

			return http.NewRequest(httpMethod, requestConfig.URL, bytes.NewBuffer(b))
		}
	}()

	e.SetRequest(request)

	if err != nil {
		e.SetMessage(err)
		return request, nil, e
	}

	// default headers
	request.Header.Set("Accept", "application/json")
	if !utilities.IsNil(requestConfig.BodyModel) {
		request.Header.Set("Content-Type", "application/json")
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
	response, e := utilities.DoWithRetry(&service.client, request, service.maxRetries, service.secondsBetweenRetries)
	if response != nil {
		if response.StatusCode < 200 || response.StatusCode > 299 {
			if e == nil {
				e = new(errortools.Error)
				e.SetRequest(request)
				e.SetResponse(response)
			}

			e.SetMessage(fmt.Sprintf("Server returned statuscode %v", response.StatusCode))
		}
	}

	if e != nil {
		if response != nil {

			defer response.Body.Close()

			b, err := ioutil.ReadAll(response.Body)
			if err == nil {
				e.SetMessage(string(b))
			}
		}

		return request, response, e
	}

	if utilities.IsNil(requestConfig.ResponseModel) {
		defer response.Body.Close()

		b, err := ioutil.ReadAll(response.Body)
		if err != nil {
			e.SetMessage(err)
			return request, response, e
		}

		err = json.Unmarshal(b, &requestConfig.ResponseModel)
		if err != nil {
			e.SetMessage(err)
			return request, response, e
		}
	}

	return request, response, nil
}

func (service *Service) get(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.httpRequest(http.MethodGet, requestConfig)
}

func (service *Service) post(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.httpRequest(http.MethodPost, requestConfig)
}

func (service *Service) put(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.httpRequest(http.MethodPut, requestConfig)
}

func (service *Service) delete(requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	return service.httpRequest(http.MethodDelete, requestConfig)
}

func unmarshalError(response *http.Response, errorModel interface{}) *errortools.Error {
	if response == nil {
		return nil
	}
	if reflect.TypeOf(errorModel).Kind() != reflect.Ptr {
		return errortools.ErrorMessage("Type of errorModel must be a pointer.")
	}
	if reflect.ValueOf(errorModel).IsNil() {
		return nil
	}

	defer response.Body.Close()

	b, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return errortools.ErrorMessage(err)
	}

	err = json.Unmarshal(b, &errorModel)
	if err != nil {
		return errortools.ErrorMessage(err)
	}

	return nil
}
