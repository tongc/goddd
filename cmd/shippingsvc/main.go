package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/go-kit/kit/log"

	kitprometheus "github.com/go-kit/kit/metrics/prometheus"

	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"gopkg.in/mgo.v2"

	shipping "github.com/tongc/goddd"
	"github.com/tongc/goddd/booking"
	"github.com/tongc/goddd/handling"
	"github.com/tongc/goddd/inmem"
	"github.com/tongc/goddd/inspection"
	"github.com/tongc/goddd/mongo"
	"github.com/tongc/goddd/routing"
	"github.com/tongc/goddd/server"
	"github.com/tongc/goddd/tracking"
)

const (
	defaultPort              = "8080"
	defaultRoutingServiceURL = "http://localhost:7878"
	defaultMongoDBURL        = "127.0.0.1"
	defaultDBName            = "dddsample"
)

var f *os.File

func main() {
	go func() {
		fmt.Println(http.ListenAndServe(":6060", nil))
	}()

	var rtm runtime.MemStats
	runtime.ReadMemStats(&rtm)

	// Just encode to json and print
	b, _ := json.Marshal(rtm)
	fmt.Println(string(b))

	var (
		addr   = envString("PORT", defaultPort)
		rsurl  = envString("ROUTINGSERVICE_URL", defaultRoutingServiceURL)
		dburl  = envString("MONGODB_URL", defaultMongoDBURL)
		dbname = envString("DB_NAME", defaultDBName)

		httpAddr          = flag.String("http.addr", ":"+addr, "HTTP listen address")
		routingServiceURL = flag.String("service.routing", rsurl, "routing service URL")
		mongoDBURL        = flag.String("db.url", dburl, "MongoDB URL")
		databaseName      = flag.String("db.name", dbname, "MongoDB database name")
		inmemory          = flag.Bool("inmem", false, "use in-memory repositories")
		cpuprofile        = flag.String("cpuprofile", "", "write cpu profile to file")
		memprofile        = flag.String("memprofile", "", "write memory profile to file")
		tracefile         = flag.String("tracefile", "", "write execution trace to file")

		ctx = context.Background()
	)

	flag.Parse()

	println(*cpuprofile, *memprofile, *tracefile)
	var logger log.Logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)

	// f3, err3 := os.Create(*tracefile)
	// if err3 != nil {
	// 	logger.Log(err3)
	// }
	// trace.Start(f3)
	// defer trace.Stop()

	// if *cpuprofile != "" {
	// 	f, err := os.Create(*cpuprofile)
	// 	if err != nil {
	// 		logger.Log(err)
	// 	}
	// 	runtime.SetCPUProfileRate(500)
	// 	pprof.StartCPUProfile(f)
	// 	f1, err1 := os.Create(*memprofile)
	// 	if err1 != nil {
	// 		logger.Log(err)
	// 	}
	// 	pprof.WriteHeapProfile(f1)
	// 	defer pprof.StopCPUProfile()
	// }

	// c := make(chan os.Signal, 2)
	// signal.Notify(c, os.Interrupt, syscall.SIGTERM) // subscribe to system signals
	// onKill := func(c chan os.Signal) {
	// 	select {
	// 	case <-c:
	// 		defer f.Close()
	// 		defer pprof.StopCPUProfile()
	// 		defer os.Exit(0)
	// 	}
	// }
	// // try to handle os interrupt(signal terminated)
	// go onKill(c)

	// Setup repositories
	var (
		cargos         shipping.CargoRepository
		locations      shipping.LocationRepository
		voyages        shipping.VoyageRepository
		handlingEvents shipping.HandlingEventRepository
	)

	if *inmemory {
		cargos = inmem.NewCargoRepository()
		locations = inmem.NewLocationRepository()
		voyages = inmem.NewVoyageRepository()
		handlingEvents = inmem.NewHandlingEventRepository()
	} else {
		session, err := mgo.Dial(*mongoDBURL)
		if err != nil {
			panic(err)
		}
		defer session.Close()

		session.SetMode(mgo.Monotonic, true)

		cargos, _ = mongo.NewCargoRepository(*databaseName, session)
		locations, _ = mongo.NewLocationRepository(*databaseName, session)
		voyages, _ = mongo.NewVoyageRepository(*databaseName, session)
		handlingEvents = mongo.NewHandlingEventRepository(*databaseName, session)
	}

	// Configure some questionable dependencies.
	var (
		handlingEventFactory = shipping.HandlingEventFactory{
			CargoRepository:    cargos,
			VoyageRepository:   voyages,
			LocationRepository: locations,
		}
		handlingEventHandler = handling.NewEventHandler(
			inspection.NewService(cargos, handlingEvents, nil),
		)
	)

	// Facilitate testing by adding some cargos.
	storeTestData(cargos)

	fieldKeys := []string{"method"}

	var rs shipping.RoutingService
	rs = routing.NewProxyingMiddleware(ctx, *routingServiceURL)(rs)

	var bs booking.Service
	bs = booking.NewService(cargos, locations, handlingEvents, rs)
	bs = booking.NewLoggingService(log.With(logger, "component", "booking"), bs)
	bs = booking.NewInstrumentingService(
		kitprometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: "api",
			Subsystem: "booking_service",
			Name:      "request_count",
			Help:      "Number of requests received.",
		}, fieldKeys),
		kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
			Namespace: "api",
			Subsystem: "booking_service",
			Name:      "request_latency_microseconds",
			Help:      "Total duration of requests in microseconds.",
		}, fieldKeys),
		bs,
	)

	var ts tracking.Service
	ts = tracking.NewService(cargos, handlingEvents)
	ts = tracking.NewLoggingService(log.With(logger, "component", "tracking"), ts)
	ts = tracking.NewInstrumentingService(
		kitprometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: "api",
			Subsystem: "tracking_service",
			Name:      "request_count",
			Help:      "Number of requests received.",
		}, fieldKeys),
		kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
			Namespace: "api",
			Subsystem: "tracking_service",
			Name:      "request_latency_microseconds",
			Help:      "Total duration of requests in microseconds.",
		}, fieldKeys),
		ts,
	)

	var hs handling.Service
	hs = handling.NewService(handlingEvents, handlingEventFactory, handlingEventHandler)
	hs = handling.NewLoggingService(log.With(logger, "component", "handling"), hs)
	hs = handling.NewInstrumentingService(
		kitprometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: "api",
			Subsystem: "handling_service",
			Name:      "request_count",
			Help:      "Number of requests received.",
		}, fieldKeys),
		kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
			Namespace: "api",
			Subsystem: "handling_service",
			Name:      "request_latency_microseconds",
			Help:      "Total duration of requests in microseconds.",
		}, fieldKeys),
		hs,
	)

	srv := server.New(bs, ts, hs, log.With(logger, "component", "http"))

	errs := make(chan error, 2)
	go func() {
		logger.Log("transport", "http", "address", *httpAddr, "msg", "listening")
		errs <- http.ListenAndServe(*httpAddr, srv)
	}()
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	logger.Log("terminated", <-errs)
}

func envString(env, fallback string) string {
	e := os.Getenv(env)
	if e == "" {
		return fallback
	}
	return e
}

func storeTestData(r shipping.CargoRepository) {
	test1 := shipping.NewCargo("FTL456", shipping.RouteSpecification{
		Origin:          shipping.AUMEL,
		Destination:     shipping.SESTO,
		ArrivalDeadline: time.Now().AddDate(0, 0, 7),
	})
	if err := r.Store(test1); err != nil {
		panic(err)
	}

	test2 := shipping.NewCargo("ABC123", shipping.RouteSpecification{
		Origin:          shipping.SESTO,
		Destination:     shipping.CNHKG,
		ArrivalDeadline: time.Now().AddDate(0, 0, 14),
	})
	if err := r.Store(test2); err != nil {
		panic(err)
	}
}
