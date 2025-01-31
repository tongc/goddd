package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/go-kit/kit/circuitbreaker"
	"github.com/go-kit/kit/endpoint"
	kithttp "github.com/go-kit/kit/transport/http"

	shipping "github.com/tongc/goddd"
)

type proxyService struct {
	context.Context
	FetchRoutesEndpoint endpoint.Endpoint
	shipping.RoutingService
}

func (s proxyService) FetchRoutesForSpecification(rs shipping.RouteSpecification) []shipping.Itinerary {
	response, err := s.FetchRoutesEndpoint(s.Context, fetchRoutesRequest{
		From: string(rs.Origin),
		To:   string(rs.Destination),
	})
	if err != nil {
		return []shipping.Itinerary{}
	}

	resp := response.(fetchRoutesResponse)

	var itineraries []shipping.Itinerary
	for _, r := range resp.Paths {
		var legs []shipping.Leg
		for _, e := range r.Edges {
			legs = append(legs, shipping.Leg{
				VoyageNumber:   shipping.VoyageNumber(e.Voyage),
				LoadLocation:   shipping.UNLocode(e.Origin),
				UnloadLocation: shipping.UNLocode(e.Destination),
				LoadTime:       e.Departure,
				UnloadTime:     e.Arrival,
			})
		}

		itineraries = append(itineraries, shipping.Itinerary{Legs: legs})
	}

	return itineraries
}

// ServiceMiddleware defines a middleware for a routing service.
type ServiceMiddleware func(shipping.RoutingService) shipping.RoutingService

// NewProxyingMiddleware returns a new instance of a proxying middleware.
func NewProxyingMiddleware(ctx context.Context, proxyURL string) ServiceMiddleware {
	return func(next shipping.RoutingService) shipping.RoutingService {
		var e endpoint.Endpoint
		e = makeFetchRoutesEndpoint(ctx, proxyURL)
		e = circuitbreaker.Hystrix("fetch-routes")(e)
		return proxyService{ctx, e, next}
	}
}

type fetchRoutesRequest struct {
	From string
	To   string
}

type fetchRoutesResponse struct {
	Paths []struct {
		Edges []struct {
			Origin      string    `json:"origin"`
			Destination string    `json:"destination"`
			Voyage      string    `json:"voyage"`
			Departure   time.Time `json:"departure"`
			Arrival     time.Time `json:"arrival"`
		} `json:"edges"`
	} `json:"paths"`
}

func makeFetchRoutesEndpoint(ctx context.Context, instance string) endpoint.Endpoint {
	u, err := url.Parse(instance)
	if err != nil {
		panic(err)
	}
	if u.Path == "" {
		u.Path = "/paths"
	}
	return kithttp.NewClient(
		"GET", u,
		encodeFetchRoutesRequest,
		decodeFetchRoutesResponse,
	).Endpoint()
}

func decodeFetchRoutesResponse(_ context.Context, resp *http.Response) (interface{}, error) {
	var response fetchRoutesResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}

func encodeFetchRoutesRequest(_ context.Context, r *http.Request, request interface{}) error {
	req := request.(fetchRoutesRequest)

	vals := r.URL.Query()
	vals.Add("from", req.From)
	vals.Add("to", req.To)
	r.URL.RawQuery = vals.Encode()

	return nil
}
