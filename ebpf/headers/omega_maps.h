#pragma once
// omega_maps.h — shared map type tags and constants

#define PROTO_HTTP1  1
#define PROTO_HTTP2  2
#define PROTO_GRPC   3

#define SK_PASS  1
#define SK_DROP  0

#define RING_SIZE  0xFFFFFFFFU  // 2^32 - 1
#define VNODES_PER_SERVER 150
