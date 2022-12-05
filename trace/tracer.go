package trace

import (
	"github.com/isyscore/isc-gobase/config"
	"github.com/isyscore/isc-gobase/isc"
	"github.com/isyscore/isc-gobase/logger"
	"github.com/isyscore/isc-gobase/store"
	_const2 "github.com/isyscore/isc-tracer/const"
	"github.com/isyscore/isc-tracer/util"
	"go.uber.org/atomic"
	"net/http"
	"strings"
	"time"
)

const ROOT_RPC_ID = "0"

var (
	copyAttrMap = map[string]string{
		_const2.TRACE_HEAD_REMOTE_APPNAME: _const2.TRACE_HEAD_REMOTE_APPNAME,
		_const2.TRACE_HEAD_REMOTE_IP:      _const2.TRACE_HEAD_REMOTE_IP,
		_const2.TRACE_HEAD_USER_ID:        _const2.A_USER_ID,
		_const2.TRACE_HEAD_USER_NAME:      _const2.A_USER_NAME,
	}
)

var (
	SwitchTrace         = true
	SwitchTraceDatabase = true
	SwitchTraceRedis    = true
	SwitchTraceEtcd     = true
)

type Tracer struct {
	// TraceId 调用链ID,一旦初始化,不能修改
	TraceId string
	// RpcId 调用顺序，依次为0 → 0.1 → 0.1.1,1 -> 1.1 -> 1.1.1 ...
	RpcId string
	// TraceType 链路跟踪类型
	TraceType _const2.TraceTypeEnum
	/**
	 * 名称
	 * 可以是一个 http url
	 * 可以是一个rpc的 service.name
	 * 可以是一个MQ的 send.{topic}.{partition}
	 * 可以是访问redis的 get.{namespace}.{key}
	 */
	TraceName string
	// Endpoint 跟踪类型
	Endpoint _const2.EndpointEnum
	// status 跟踪结果
	status _const2.TraceStatusEnum

	// RemoteStatus 远程调用结果
	RemoteStatus _const2.TraceStatusEnum
	// RemoteIp 远程调用IP,即下游(Client)或上游(Server)ip
	RemoteIp string
	// message 调用返回或异常信息
	message string
	// Size 响应体大小
	Size int

	// StartTime 当前span开始时间
	StartTime int64
	// endTime 当前span结束时间
	endTime int64
	// 是否采样
	Sampled bool
	// bizData 响应数据
	bizData map[string]any
	// 是否已经结束
	Ended bool
	// AttrMap 请求参数
	AttrMap map[string]string
	//  子rpc id的自增器
	ChildRpcSeq atomic.Int32
}

func doStartTrace(traceId string, rpcId string, traceType _const2.TraceTypeEnum, traceName string, endpoint _const2.EndpointEnum) *Tracer {
	if !TracerIsEnable() {
		return nil
	}
	if rpcId == "" {
		rpcId = ROOT_RPC_ID
	}

	tracer := createCurrentTracerIfAbsent()
	if !tracer.Ended {
		if tracer.TraceId == traceId {
			return tracer
		}
	} else if endpoint == _const2.CLIENT {
		childTracer := newTracer(traceId, rpcId, traceType, traceName, endpoint)
		if tracer.TraceId != "" {
			// 0 -> 0.1 -> 0.1.1
			childTracer.RpcId = tracer.RpcId + "." + isc.ToString(tracer.ChildRpcSeq.Inc())
			childTracer.Sampled = tracer.Sampled
		}
		setTrace(childTracer)
		return childTracer
	} else if tracer.TraceId != "" {
		return tracer
	}
	tracer = newTracer(traceId, rpcId, traceType, traceName, endpoint)
	setTrace(tracer)
	return tracer
}

func newTracer(traceId string, rpcId string, traceType _const2.TraceTypeEnum, traceName string, endpoint _const2.EndpointEnum) *Tracer {
	tracer := &Tracer{
		TraceId:   traceId,
		RpcId:     rpcId,
		TraceType: traceType,
		TraceName: traceName,
		Endpoint:  endpoint,
	}
	tracer.startTrace()
	return tracer
}
func (tracer *Tracer) startTrace() {
	tracer.Sampled = true
	tracer.StartTime = time.Now().UnixMilli()
	tracer.AttrMap = make(map[string]string)
}

func (tracer *Tracer) EndTrace(status _const2.TraceStatusEnum, message string, responseSize int) {
	defer func() {
		deleteTrace(tracer.RpcId)
	}()
	if !TracerIsEnable() || tracer.Ended {
		return
	}

	if tracer.TraceId == "" || tracer.RpcId == "" || tracer.StartTime == 0 {
		//log.Println("tracer's traceId is nil,will be not append tracer info")
		return
	}
	tracer.Ended = true
	if tracer.getStatus() == _const2.OK && !tracer.Sampled {
		return
	}

	if req := store.GetRequest(); req != nil {
		header := req.Header
		putAttr(tracer, &header)
	}

	tracer.endTime = time.Now().UnixMilli()
	tracer.status = status
	tracer.Size = responseSize
	if message != "" {
		tracer.message = message
	}
	SendTraceLog(tracer)
}

func (tracer *Tracer) getStatus() _const2.TraceStatusEnum {
	if tracer.status != _const2.OK {
		return tracer.status
	}
	if tracer.RemoteStatus != _const2.OK {
		return tracer.RemoteStatus
	}
	return _const2.OK
}

func TracerIsEnable() bool {
	return config.GetValueBoolDefault("tracer.enable", true) && SwitchTrace
}

// StartTrace
// traceName 名称
//    可以是一个 http url
//    可以是一个rpc的 service.name
//    可以是一个MQ的 send.{topic}.{partition}
//    可以是访问redis的 get.{namespace}.{key}
func StartTrace(traceType _const2.TraceTypeEnum, endPoint _const2.EndpointEnum, traceName string, request *http.Request) *Tracer {
	if !TracerIsEnable() {
		return nil
	}
	remoteAddr := store.GetRemoteAddr()
	if request == nil {
		request = store.GetRequest()
	}
	var tracerId string
	var rpcId string
	if request != nil {
		tracerId = request.Header.Get(_const2.TRACE_HEAD_ID)
		rpcId = request.Header.Get(_const2.TRACE_HEAD_RPC_ID)
	}

	frontIP := ""
	if tracerId == "" {
		tracerId = util.GenerateTraceId()
		if request != nil {
			frontIP = GetFrontIP(&request.Header, remoteAddr)
		}
	}

	tracer := doStartTrace(tracerId, rpcId, traceType, traceName, endPoint)
	if tracer == nil {
		return nil
	}

	rpcId = tracer.RpcId
	if request != nil {
		request.Header.Set(_const2.TRACE_HEAD_ID, tracerId)
		request.Header.Set(_const2.TRACE_HEAD_RPC_ID, rpcId)
	}

	store.RequestHeadSet(_const2.TRACE_HEAD_ID, tracerId)
	store.RequestHeadSet(_const2.TRACE_HEAD_RPC_ID, rpcId)

	logger.PutMdc(_const2.TRACE_HEAD_ID, tracerId)
	logger.PutMdc(_const2.TRACE_HEAD_RPC_ID, rpcId)

	if frontIP != "" {
		tracer.RemoteIp = frontIP
	}
	// 往当前上下文添加远程端属性
	if request != nil {
		putAttr(tracer, &request.Header)
	}
	return tracer
}

func StartTraceWithHeader(traceType _const2.TraceTypeEnum, endPoint _const2.EndpointEnum, traceName string, header *http.Header) *Tracer {
	if !TracerIsEnable() {
		return nil
	}
	remoteAddr := store.GetRemoteAddr()
	var tracerId string
	var rpcId string
	if header != nil {
		tracerId = header.Get(_const2.TRACE_HEAD_ID)
		rpcId = header.Get(_const2.TRACE_HEAD_RPC_ID)
	}

	frontIP := ""
	if tracerId == "" {
		tracerId = util.GenerateTraceId()
		if header != nil {
			frontIP = GetFrontIP(header, remoteAddr)
		}
	}

	tracer := doStartTrace(tracerId, rpcId, traceType, traceName, endPoint)
	if tracer == nil {
		return nil
	}

	rpcId = tracer.RpcId
	if header != nil {
		header.Set(_const2.TRACE_HEAD_ID, tracerId)
		header.Set(_const2.TRACE_HEAD_RPC_ID, rpcId)
	}

	store.RequestHeadSet(_const2.TRACE_HEAD_ID, tracerId)
	store.RequestHeadSet(_const2.TRACE_HEAD_RPC_ID, rpcId)

	logger.PutMdc(_const2.TRACE_HEAD_ID, tracerId)
	logger.PutMdc(_const2.TRACE_HEAD_RPC_ID, rpcId)

	if frontIP != "" {
		tracer.RemoteIp = frontIP
	}
	// 往当前上下文添加远程端属性
	if header != nil {
		putAttr(tracer, header)
	}
	return tracer
}

func EndTrace(tracer *Tracer, status _const2.TraceStatusEnum, message string, responseSize int) {
	tracer.EndTrace(status, message, responseSize)
}

func putAttr(tracer *Tracer, head *http.Header) {
	if head == nil {
		return
	}
	if tracer.AttrMap == nil {
		tracer.AttrMap = make(map[string]string)
	}
	for key, copyKey := range copyAttrMap {
		if v := head.Get(key); v != "" {
			tracer.AttrMap[copyKey] = v
		}
	}
}

func GetFrontIP(head *http.Header, remoteAddr string) string {
	if head == nil {
		return ""
	}
	ip := head.Get("X-Forwarded-For")
	if ip != "" && strings.EqualFold(ip, "unKnown") {
		//多次反向代理后会有多个ip值，第一个ip才是真实ip
		if i := strings.Index(ip, ","); i != -1 {
			return ip[:i]
		}
		return ip
	}
	ip = head.Get("X-Real-IP")
	if ip != "" && strings.EqualFold(ip, "unKnown") {
		return ip
	}
	return remoteAddr
}
