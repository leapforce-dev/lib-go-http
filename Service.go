package http

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	errortools "github.com/leapforce-libraries/go_errortools"
	utilities "github.com/leapforce-libraries/go_utilities"
)

type Accept string

const (
	AcceptJSON Accept = "json"
	AcceptXML  Accept = "xml"
)

type Service struct {
	accept Accept
	client http.Client
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
	BodyModel          interface{}
	ResponseModel      interface{}
	ErrorModel         interface{}
	NonDefaultHeaders  *http.Header
	XWWWFormURLEncoded *bool
	MaxRetries         *uint
}

func (service *Service) HTTPRequest(httpMethod string, requestConfig *RequestConfig) (*http.Request, *http.Response, *errortools.Error) {
	e := new(errortools.Error)

	request, err := func() (*http.Request, error) {
		if utilities.IsNil(requestConfig.BodyModel) {
			return http.NewRequest(httpMethod, requestConfig.URL, nil)
		}

		if requestConfig.XWWWFormURLEncoded != nil {
			if *requestConfig.XWWWFormURLEncoded {
				tag := "json"
				url, e := utilities.StructToURL(&requestConfig.BodyModel, &tag)
				if e != nil {
					return nil, errors.New(e.Message())
				}

				return http.NewRequest(httpMethod, requestConfig.URL, strings.NewReader(*url))
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

		return http.NewRequest(httpMethod, requestConfig.URL, bytes.NewBuffer(b))

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
	response, e := utilities.DoWithRetry(&service.client, request, requestConfig.MaxRetries)

	if response != nil {
		if response.StatusCode < 200 || response.StatusCode > 299 {
			if e == nil {
				e = new(errortools.Error)
			}
			e.SetRequest(request)
			e.SetResponse(response)
			e.SetMessage(fmt.Sprintf("Server returned statuscode %v", response.StatusCode))
		}

		if response.Body != nil {
			if !utilities.IsNil(requestConfig.ResponseModel) {

				defer response.Body.Close()

				b, err := ioutil.ReadAll(response.Body)
				if err != nil {
					if e == nil {
						e = new(errortools.Error)
					}
					e.SetRequest(request)
					e.SetResponse(response)
					e.SetMessage(err)
					return request, response, e
				}

				if e != nil {
					if !utilities.IsNil(requestConfig.ErrorModel) {
						// try to unmarshal to ErrorModel
						var errError error
						if service.accept == AcceptXML {
							errError = xml.Unmarshal(b, &requestConfig.ErrorModel)
						} else {
							errError = json.Unmarshal(b, &requestConfig.ErrorModel)
						}
						if errError != nil {
							e.SetExtra("response_message", string(b))
						}
					}

					return request, response, e
				}

				//if !utilities.IsNil(requestConfig.ResponseModel) {
				//var err error
				if service.accept == AcceptXML {
					err = xml.Unmarshal(b, &requestConfig.ResponseModel)
				} else {
					err = json.Unmarshal(b, &requestConfig.ResponseModel)
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
				//}
			}
		}
	}

	return request, response, nil
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
