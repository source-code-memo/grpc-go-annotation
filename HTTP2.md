## HTTP2
HTTP2 协议相比于HTTP协议有了很多的改进。也是一个未来会替代HTTP1.x的协议。

HTTP2 起源于SPDY。

HTTP2 的RFC文档主要由两部分组成：

[RFC7541](http://httpwg.org/specs/rfc7541.html)

[RFC7540](http://httpwg.org/specs/rfc7540.html)

RFC7541 主要是HTTP2 头部压缩协议- HPACK

RFC7540 主要是HTTP2 流协议

## HPACK: HTTP2 的头部压缩格式
在HTTP1.x中, 头部是没有压缩的。当请求数很大时，头部带来的带宽和延迟的消耗也是很大的。
SPDY最开始提出使用[DEFLATE](https://tools.ietf.org/html/rfc1951)格式来进行压缩,但是这个方法被指出有CRIME(Compress Ratio Info-leak Made Easy)攻击的安全风险。
目前HTTP2中的使用的是HPACK压缩格式


