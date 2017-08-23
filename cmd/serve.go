package main

import (
	"context"
	middleware "github.com/dafiti/echo-middleware"
	inst "github.com/dafiti/go-instrument"
	"github.com/labstack/echo"
	mw "github.com/labstack/echo/middleware"
	"github.com/labstack/gommon/color"
	"github.com/labstack/gommon/log"
	api "github.com/pintobikez/correios-service/api"
	uti "github.com/pintobikez/correios-service/config"
	strut "github.com/pintobikez/correios-service/config/structures"
	cronjob "github.com/pintobikez/correios-service/cronjob"
	lg "github.com/pintobikez/correios-service/log"
	rep "github.com/pintobikez/correios-service/repository/mysql"
	srv "github.com/pintobikez/correios-service/server"
	"github.com/robfig/cron"
	"gopkg.in/urfave/cli.v1"
	"os"
	"os/signal"
	"strconv"
	"time"
)

var (
	instrument inst.Instrument = new(inst.Dummy)
	repo       *rep.Repository = new(rep.Repository)
)

// Start Http Server
func Serve(c *cli.Context) error {

	// Echo instance
	e := &srv.Server{echo.New()}
	e.HTTPErrorHandler = api.Error
	e.Logger.SetLevel(log.INFO)
	e.Logger.SetOutput(lg.File(c.String("log-folder") + "/app.log"))

	// Middlewares
	e.Use(middleware.LoggerWithOutput(lg.File(c.String("log-folder") + "/access.log")))

	if c.String("newrelic-appname") != "" && c.String("newrelic-license-key") != "" {
		e.Use(middleware.NewRelic(
			c.String("newrelic-appname"),
			c.String("newrelic-license-key"),
		))

		instrument = new(inst.NewRelic)
	}

	e.Use(mw.Recover())
	e.Use(mw.Secure())
	e.Use(mw.RequestID())
	e.Pre(mw.RemoveTrailingSlash())

	//loads db connection
	err, stringConn := buildStringConnection(c.String("database-file"))
	if err != nil {
		e.Logger.Fatal(err)
	}

	// Database connect
	err = repo.ConnectDB(stringConn)
	if err != nil {
		e.Logger.Fatal(err)
	}
	defer repo.DisconnectDB()

	//loads correios config
	err, correiosCnf := buildCorreiosConfig(c.String("correios-file"))
	if err != nil {
		e.Logger.Fatal(err)
	}

	a := api.New(repo, correiosCnf)
	cj := cronjob.New(repo, correiosCnf)

	// Routes => api
	e.POST("/tracking", a.GetTracking())
	e.POST("/reverse", a.PostReverse())
	e.POST("/reversesearch", a.GetReversesBy())
	e.PUT("/reverse/:requestId", a.PutReverse())
	e.DELETE("/reverse/:requestId", a.DeleteReverse())
	e.GET("/reverse/:requestId", a.GetReverse())

	if c.String("revision-file") != "" {
		e.File("/rev.txt", c.String("revision-file"))
	}

	if swagger := c.String("swagger-file"); swagger != "" {
		g := e.Group("/docs")
		g.Use(mw.CORSWithConfig(
			mw.CORSConfig{
				AllowOrigins: []string{"http://petstore.swagger.io"},
				AllowMethods: []string{echo.GET, echo.HEAD},
			},
		))

		g.GET("", func(c echo.Context) error {
			return c.File(swagger)
		})
	}

	// Start server
	colorer := color.New()
	colorer.Printf("⇛ %s service - %s\n", appName, color.Green(version))
	//Print available routes
	colorer.Printf("⇛ Available Routes:\n")
	for _, rou := range e.Routes() {
		colorer.Printf("⇛ URI: [%s] %s\n", color.Green(rou.Method), color.Green(rou.Path))
	}

	go func() {
		if err := start(e, c); err != nil {
			colorer.Printf(color.Red("⇛ shutting down the server\n"))
		}
	}()

	// launch a cron to check everyday for posted items
	cr := cron.New()
	cr.AddFunc("* 0 */6 * * *", func() { cj.CheckUpdatedReverses("C") })     // checks for Colect updates
	cr.AddFunc("* 10 */6 * * *", func() { cj.CheckUpdatedReverses("A") })    // checks for Postage updates
	cr.AddFunc("* */20 * * * *", func() { cj.ReprocessRequestsWithError() }) // checks for Requests with Error and reprocesses them
	cr.Start()
	defer cr.Stop()

	// Graceful Shutdown
	quit := make(chan os.Signal)
	signal.Notify(quit, os.Interrupt)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal(err)
	}

	return nil
}

// Start http server
func start(e *srv.Server, c *cli.Context) error {
	return e.Start(c.String("listen"))
}
func buildCorreiosConfig(filename string) (error, *strut.CorreiosConfig) {
	t := new(strut.CorreiosConfig)
	err := uti.LoadCorreiosConfigFile(filename, t)
	if err != nil {
		return err, nil
	}
	return nil, t
}

func buildStringConnection(filename string) (error, string) {
	t := new(strut.DbConfig)
	err := uti.LoadDBConfigFile(filename, t)
	if err != nil {
		return err, ""
	}
	// [username[:password]@][protocol[(address)]]/dbname[?param1=value1&...&paramN=valueN]
	stringConn := t.Driver.User + ":" + t.Driver.Pw
	stringConn += "@tcp(" + t.Driver.Host + ":" + strconv.Itoa(t.Driver.Port) + ")"
	stringConn += "/" + t.Driver.Schema + "?charset=utf8"

	return nil, stringConn
}