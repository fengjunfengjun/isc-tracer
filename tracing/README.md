# trace

```yaml
tracer:
    # 链路中间件采集总开关；默认开启；
    enable: true
    # 服务搜集库；默认开启；
    collector-endpoint: http://isc-core-back-service:31300/api/core/back/v1/middle/spans
    # 是否开启链路相关日志打印，默认关闭
    print-log: false
    orm:
      # 是否启动gorm采集；该开关与总开关是与的关系；目前go只支持gorm和xorm；默认关闭
      enable: false
    redis:
      # 是否启动redis采集（go-redis客户端）；默认关闭
      enable: false
```
