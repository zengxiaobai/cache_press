客户端：
./cache_press -mode=client -addr=192.168.233.43:8081 -conns=1000 -qps=3000 -duration=600s -hit-ratio=0.85 -url-count=1000000 -resp-size=[1024,4096] -disk-ratio=0.7 -host test.com -defer-start=3

服务端：
./cache_press -mode=server -port=9000


TODO:
1. 客户端和回源头部校验，对部分 或者所有头做一致性校验，或者配置排除某些头部
2. 源站打印日志，支持trace id
3. 支持body 长度 以及 md5 校验 （分片缓存）
