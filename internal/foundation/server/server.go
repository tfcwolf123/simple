//go:build linux
// +build linux

package server

import (
	"crypto/tls"
	"fmt"
	"github.com/facebookgo/grace/gracehttp"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/sirupsen/logrus"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"simple/foundation/app"
	app2 "simple/internal/foundation/app"
	"simple/internal/foundation/database/orm"
	"simple/internal/foundation/engine"
	"simple/internal/foundation/log"
	"simple/internal/foundation/validator"
	"simple/internal/foundation/view"
	"strings"
	"time"
)

type (
	application struct {
		Name          string `toml:"name"`
		Schema        string `toml:"schema"`
		Domain        string `toml:"domain"`
		Addr          string `toml:"addr"`
		PasswordToken string `toml:"password_token"`
		JwtToken      string `toml:"jwt-token"`
		CertFile      string `toml:"cert_file"`
		KeyFile       string `toml:"key_file"`
	}
)

var (
	pidFile = fmt.Sprintf("./%s.pid", app2.Name())
	// Mode 当前服务名, 暂时需要这么个东西, 按目前的结构获得指定配置信息等操作
	Mode string
	// After 在各项服务启动之后会执行的操作
	After       func(engine *gin.Engine)
	swagHandler gin.HandlerFunc
	router      = gin.New()

	// Config 服务配置
	Config application
)

func certInfo() (string, string) {
	return Config.CertFile, Config.KeyFile
}

// 启动各项服务
func start() {
	log.Start()
	orm.Start()
	//mongo.Start()
	//mgo.Start()
	//redis.Start()
	//elastic.Start()
	view.Init()
	view.View.AddPath("/view/" + Mode + "/")
	// 加载应用配置
	err := app2.Config().Bind("application", fmt.Sprintf("application.%s", Mode), &Config)
	if err != nil {
		fmt.Println(err)
	}
	// 将 gin 的验证器替换为 v9 版本
	binding.Validator = new(validator.Validator)
}

//Run 启动服务
func Run(router func(engine engine.Engine)) {
	//lock := createPid()
	//defer lock.UnLock()

	start()
	app2.Logger().WithField("log_type", "foundation.server.server").Info("server started at:", time.Now().String())
	engineInst := engine.GetEngine()
	router(engineInst)

	//if swagHandler != nil && gin.Mode() != gin.ReleaseMode {
	//	engine.GET("/doc/*any", swagHandler)
	//}
	//createServer(engineInst.GetHandler()).ListenAndServe()
	_ = gracehttp.ServeWithOptions([]*http.Server{createServer(engineInst.GetHandler())}, gracehttp.PreStartProcess(func() error {
		app2.Logger().WithField("log_type", "foundation.server.server").Println("unlock pid")
		//lock.
		return nil
	}))
}

func createServer(router http.Handler) *http.Server {
	server := &http.Server{
		Addr:    Config.Addr,
		Handler: router,
	}

	if certFile, certKey := certInfo(); certFile != "" && certKey != "" {
		server.TLSConfig = &tls.Config{}
		f, _ := tls.LoadX509KeyPair(certFile, certKey)
		server.TLSConfig.Certificates = []tls.Certificate{f}
	}

	return server
}

// 对启动进程记录进程id
func createPid() *app.Flock {
	pidLock, pidLockErr := app.FLock(pidFile)
	if pidLockErr != nil {
		app2.Logger().WithField("log_type", "foundation.server.server").Fatalln("createPid: lock error", pidLockErr)
	}

	err := pidLock.WriteTo(fmt.Sprintf(`%d`, os.Getpid()))
	if err != nil {
		app2.Logger().WithField("log_type", "foundation.server.server").Fatalln("write error: ", err)
	}
	return pidLock
}

// 自定义的GIN日志处理中间件
// 可能在终端输出时看起来比较的难看
func logger(ctx *gin.Context) {
	start := time.Now()
	path := ctx.Request.URL.Path
	raw := ctx.Request.URL.RawQuery

	ctx.Next()

	if raw != "" {
		path = path + "?" + raw
	}

	var params = make(logrus.Fields)
	params["latency"] = time.Now().Sub(start)
	params["path"] = path
	params["method"] = ctx.Request.Method
	params["status"] = ctx.Writer.Status()
	params["body_size"] = ctx.Writer.Size()
	params["client_ip"] = ctx.ClientIP()
	params["user_agent"] = ctx.Request.UserAgent()
	params["log_type"] = "foundation.server.server"
	if !gin.IsDebugging() {
		// 在正式环境将上下文传递的变量也进行记录, 方便分析
		params["keys"] = ctx.Keys
	}
	app2.Logger().WithFields(params).Info("request success, status is ", ctx.Writer.Status(), ", client ip is ", ctx.ClientIP())
}

func recovery(ctx *gin.Context) {
	defer func() {
		if err := recover(); err != nil {
			var brokenPipe bool
			if ne, ok := err.(*net.OpError); ok {
				if se, ok := ne.Err.(*os.SyscallError); ok {
					if strings.Contains(strings.ToLower(se.Error()), "broken pipe") || strings.Contains(strings.ToLower(se.Error()), "connection reset by peer") {
						brokenPipe = true
					}
				}
			}
			stack := app2.Stack(3)
			httpRequest, _ := httputil.DumpRequest(ctx.Request, false)

			if gin.IsDebugging() {
				app2.Logger().WithField("log_type", "foundation.server.server").Error(string(httpRequest))
				var errors = make([]logrus.Fields, 0)
				for i := 0; i < len(stack); i++ {
					errors = append(errors, logrus.Fields{
						"func":   stack[i]["func"],
						"source": stack[i]["source"],
						"file":   fmt.Sprintf("%s:%d", stack[i]["file"], stack[i]["line"]),
					})
				}
				app2.Logger().WithField("log_type", "foundation.server.server").WithField("stack", errors).Error(err)
				ctx.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"stack": errors, "message": err})
			} else {
				app2.Logger().WithField("log_type", "foundation.server.server").
					WithField("stack", stack).WithField("request", string(httpRequest)).Error()
			}

			if brokenPipe {
				_ = ctx.Error(err.(error))
				ctx.Abort()
			} else {
				ctx.AbortWithStatus(http.StatusInternalServerError)
			}
		}
	}()
	ctx.Next()
}
