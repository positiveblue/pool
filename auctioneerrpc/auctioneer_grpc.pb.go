// Code generated by protoc-gen-go-grpc. DO NOT EDIT.

package auctioneerrpc

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// ChannelAuctioneerClient is the client API for ChannelAuctioneer service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ChannelAuctioneerClient interface {
	ReserveAccount(ctx context.Context, in *ReserveAccountRequest, opts ...grpc.CallOption) (*ReserveAccountResponse, error)
	InitAccount(ctx context.Context, in *ServerInitAccountRequest, opts ...grpc.CallOption) (*ServerInitAccountResponse, error)
	ModifyAccount(ctx context.Context, in *ServerModifyAccountRequest, opts ...grpc.CallOption) (*ServerModifyAccountResponse, error)
	SubmitOrder(ctx context.Context, in *ServerSubmitOrderRequest, opts ...grpc.CallOption) (*ServerSubmitOrderResponse, error)
	CancelOrder(ctx context.Context, in *ServerCancelOrderRequest, opts ...grpc.CallOption) (*ServerCancelOrderResponse, error)
	OrderState(ctx context.Context, in *ServerOrderStateRequest, opts ...grpc.CallOption) (*ServerOrderStateResponse, error)
	SubscribeBatchAuction(ctx context.Context, opts ...grpc.CallOption) (ChannelAuctioneer_SubscribeBatchAuctionClient, error)
	SubscribeSidecar(ctx context.Context, opts ...grpc.CallOption) (ChannelAuctioneer_SubscribeSidecarClient, error)
	Terms(ctx context.Context, in *TermsRequest, opts ...grpc.CallOption) (*TermsResponse, error)
	RelevantBatchSnapshot(ctx context.Context, in *RelevantBatchRequest, opts ...grpc.CallOption) (*RelevantBatch, error)
	BatchSnapshot(ctx context.Context, in *BatchSnapshotRequest, opts ...grpc.CallOption) (*BatchSnapshotResponse, error)
	NodeRating(ctx context.Context, in *ServerNodeRatingRequest, opts ...grpc.CallOption) (*ServerNodeRatingResponse, error)
	BatchSnapshots(ctx context.Context, in *BatchSnapshotsRequest, opts ...grpc.CallOption) (*BatchSnapshotsResponse, error)
	MarketInfo(ctx context.Context, in *MarketInfoRequest, opts ...grpc.CallOption) (*MarketInfoResponse, error)
}

type channelAuctioneerClient struct {
	cc grpc.ClientConnInterface
}

func NewChannelAuctioneerClient(cc grpc.ClientConnInterface) ChannelAuctioneerClient {
	return &channelAuctioneerClient{cc}
}

func (c *channelAuctioneerClient) ReserveAccount(ctx context.Context, in *ReserveAccountRequest, opts ...grpc.CallOption) (*ReserveAccountResponse, error) {
	out := new(ReserveAccountResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/ReserveAccount", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) InitAccount(ctx context.Context, in *ServerInitAccountRequest, opts ...grpc.CallOption) (*ServerInitAccountResponse, error) {
	out := new(ServerInitAccountResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/InitAccount", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) ModifyAccount(ctx context.Context, in *ServerModifyAccountRequest, opts ...grpc.CallOption) (*ServerModifyAccountResponse, error) {
	out := new(ServerModifyAccountResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/ModifyAccount", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) SubmitOrder(ctx context.Context, in *ServerSubmitOrderRequest, opts ...grpc.CallOption) (*ServerSubmitOrderResponse, error) {
	out := new(ServerSubmitOrderResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/SubmitOrder", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) CancelOrder(ctx context.Context, in *ServerCancelOrderRequest, opts ...grpc.CallOption) (*ServerCancelOrderResponse, error) {
	out := new(ServerCancelOrderResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/CancelOrder", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) OrderState(ctx context.Context, in *ServerOrderStateRequest, opts ...grpc.CallOption) (*ServerOrderStateResponse, error) {
	out := new(ServerOrderStateResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/OrderState", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) SubscribeBatchAuction(ctx context.Context, opts ...grpc.CallOption) (ChannelAuctioneer_SubscribeBatchAuctionClient, error) {
	stream, err := c.cc.NewStream(ctx, &ChannelAuctioneer_ServiceDesc.Streams[0], "/poolrpc.ChannelAuctioneer/SubscribeBatchAuction", opts...)
	if err != nil {
		return nil, err
	}
	x := &channelAuctioneerSubscribeBatchAuctionClient{stream}
	return x, nil
}

type ChannelAuctioneer_SubscribeBatchAuctionClient interface {
	Send(*ClientAuctionMessage) error
	Recv() (*ServerAuctionMessage, error)
	grpc.ClientStream
}

type channelAuctioneerSubscribeBatchAuctionClient struct {
	grpc.ClientStream
}

func (x *channelAuctioneerSubscribeBatchAuctionClient) Send(m *ClientAuctionMessage) error {
	return x.ClientStream.SendMsg(m)
}

func (x *channelAuctioneerSubscribeBatchAuctionClient) Recv() (*ServerAuctionMessage, error) {
	m := new(ServerAuctionMessage)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *channelAuctioneerClient) SubscribeSidecar(ctx context.Context, opts ...grpc.CallOption) (ChannelAuctioneer_SubscribeSidecarClient, error) {
	stream, err := c.cc.NewStream(ctx, &ChannelAuctioneer_ServiceDesc.Streams[1], "/poolrpc.ChannelAuctioneer/SubscribeSidecar", opts...)
	if err != nil {
		return nil, err
	}
	x := &channelAuctioneerSubscribeSidecarClient{stream}
	return x, nil
}

type ChannelAuctioneer_SubscribeSidecarClient interface {
	Send(*ClientAuctionMessage) error
	Recv() (*ServerAuctionMessage, error)
	grpc.ClientStream
}

type channelAuctioneerSubscribeSidecarClient struct {
	grpc.ClientStream
}

func (x *channelAuctioneerSubscribeSidecarClient) Send(m *ClientAuctionMessage) error {
	return x.ClientStream.SendMsg(m)
}

func (x *channelAuctioneerSubscribeSidecarClient) Recv() (*ServerAuctionMessage, error) {
	m := new(ServerAuctionMessage)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *channelAuctioneerClient) Terms(ctx context.Context, in *TermsRequest, opts ...grpc.CallOption) (*TermsResponse, error) {
	out := new(TermsResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/Terms", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) RelevantBatchSnapshot(ctx context.Context, in *RelevantBatchRequest, opts ...grpc.CallOption) (*RelevantBatch, error) {
	out := new(RelevantBatch)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/RelevantBatchSnapshot", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) BatchSnapshot(ctx context.Context, in *BatchSnapshotRequest, opts ...grpc.CallOption) (*BatchSnapshotResponse, error) {
	out := new(BatchSnapshotResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/BatchSnapshot", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) NodeRating(ctx context.Context, in *ServerNodeRatingRequest, opts ...grpc.CallOption) (*ServerNodeRatingResponse, error) {
	out := new(ServerNodeRatingResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/NodeRating", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) BatchSnapshots(ctx context.Context, in *BatchSnapshotsRequest, opts ...grpc.CallOption) (*BatchSnapshotsResponse, error) {
	out := new(BatchSnapshotsResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/BatchSnapshots", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *channelAuctioneerClient) MarketInfo(ctx context.Context, in *MarketInfoRequest, opts ...grpc.CallOption) (*MarketInfoResponse, error) {
	out := new(MarketInfoResponse)
	err := c.cc.Invoke(ctx, "/poolrpc.ChannelAuctioneer/MarketInfo", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChannelAuctioneerServer is the server API for ChannelAuctioneer service.
// All implementations must embed UnimplementedChannelAuctioneerServer
// for forward compatibility
type ChannelAuctioneerServer interface {
	ReserveAccount(context.Context, *ReserveAccountRequest) (*ReserveAccountResponse, error)
	InitAccount(context.Context, *ServerInitAccountRequest) (*ServerInitAccountResponse, error)
	ModifyAccount(context.Context, *ServerModifyAccountRequest) (*ServerModifyAccountResponse, error)
	SubmitOrder(context.Context, *ServerSubmitOrderRequest) (*ServerSubmitOrderResponse, error)
	CancelOrder(context.Context, *ServerCancelOrderRequest) (*ServerCancelOrderResponse, error)
	OrderState(context.Context, *ServerOrderStateRequest) (*ServerOrderStateResponse, error)
	SubscribeBatchAuction(ChannelAuctioneer_SubscribeBatchAuctionServer) error
	SubscribeSidecar(ChannelAuctioneer_SubscribeSidecarServer) error
	Terms(context.Context, *TermsRequest) (*TermsResponse, error)
	RelevantBatchSnapshot(context.Context, *RelevantBatchRequest) (*RelevantBatch, error)
	BatchSnapshot(context.Context, *BatchSnapshotRequest) (*BatchSnapshotResponse, error)
	NodeRating(context.Context, *ServerNodeRatingRequest) (*ServerNodeRatingResponse, error)
	BatchSnapshots(context.Context, *BatchSnapshotsRequest) (*BatchSnapshotsResponse, error)
	MarketInfo(context.Context, *MarketInfoRequest) (*MarketInfoResponse, error)
	mustEmbedUnimplementedChannelAuctioneerServer()
}

// UnimplementedChannelAuctioneerServer must be embedded to have forward compatible implementations.
type UnimplementedChannelAuctioneerServer struct {
}

func (UnimplementedChannelAuctioneerServer) ReserveAccount(context.Context, *ReserveAccountRequest) (*ReserveAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ReserveAccount not implemented")
}
func (UnimplementedChannelAuctioneerServer) InitAccount(context.Context, *ServerInitAccountRequest) (*ServerInitAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method InitAccount not implemented")
}
func (UnimplementedChannelAuctioneerServer) ModifyAccount(context.Context, *ServerModifyAccountRequest) (*ServerModifyAccountResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ModifyAccount not implemented")
}
func (UnimplementedChannelAuctioneerServer) SubmitOrder(context.Context, *ServerSubmitOrderRequest) (*ServerSubmitOrderResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SubmitOrder not implemented")
}
func (UnimplementedChannelAuctioneerServer) CancelOrder(context.Context, *ServerCancelOrderRequest) (*ServerCancelOrderResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CancelOrder not implemented")
}
func (UnimplementedChannelAuctioneerServer) OrderState(context.Context, *ServerOrderStateRequest) (*ServerOrderStateResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method OrderState not implemented")
}
func (UnimplementedChannelAuctioneerServer) SubscribeBatchAuction(ChannelAuctioneer_SubscribeBatchAuctionServer) error {
	return status.Errorf(codes.Unimplemented, "method SubscribeBatchAuction not implemented")
}
func (UnimplementedChannelAuctioneerServer) SubscribeSidecar(ChannelAuctioneer_SubscribeSidecarServer) error {
	return status.Errorf(codes.Unimplemented, "method SubscribeSidecar not implemented")
}
func (UnimplementedChannelAuctioneerServer) Terms(context.Context, *TermsRequest) (*TermsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Terms not implemented")
}
func (UnimplementedChannelAuctioneerServer) RelevantBatchSnapshot(context.Context, *RelevantBatchRequest) (*RelevantBatch, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RelevantBatchSnapshot not implemented")
}
func (UnimplementedChannelAuctioneerServer) BatchSnapshot(context.Context, *BatchSnapshotRequest) (*BatchSnapshotResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method BatchSnapshot not implemented")
}
func (UnimplementedChannelAuctioneerServer) NodeRating(context.Context, *ServerNodeRatingRequest) (*ServerNodeRatingResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method NodeRating not implemented")
}
func (UnimplementedChannelAuctioneerServer) BatchSnapshots(context.Context, *BatchSnapshotsRequest) (*BatchSnapshotsResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method BatchSnapshots not implemented")
}
func (UnimplementedChannelAuctioneerServer) MarketInfo(context.Context, *MarketInfoRequest) (*MarketInfoResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method MarketInfo not implemented")
}
func (UnimplementedChannelAuctioneerServer) mustEmbedUnimplementedChannelAuctioneerServer() {}

// UnsafeChannelAuctioneerServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ChannelAuctioneerServer will
// result in compilation errors.
type UnsafeChannelAuctioneerServer interface {
	mustEmbedUnimplementedChannelAuctioneerServer()
}

func RegisterChannelAuctioneerServer(s grpc.ServiceRegistrar, srv ChannelAuctioneerServer) {
	s.RegisterService(&ChannelAuctioneer_ServiceDesc, srv)
}

func _ChannelAuctioneer_ReserveAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ReserveAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).ReserveAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/ReserveAccount",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).ReserveAccount(ctx, req.(*ReserveAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_InitAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerInitAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).InitAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/InitAccount",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).InitAccount(ctx, req.(*ServerInitAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_ModifyAccount_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerModifyAccountRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).ModifyAccount(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/ModifyAccount",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).ModifyAccount(ctx, req.(*ServerModifyAccountRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_SubmitOrder_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerSubmitOrderRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).SubmitOrder(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/SubmitOrder",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).SubmitOrder(ctx, req.(*ServerSubmitOrderRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_CancelOrder_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerCancelOrderRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).CancelOrder(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/CancelOrder",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).CancelOrder(ctx, req.(*ServerCancelOrderRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_OrderState_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerOrderStateRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).OrderState(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/OrderState",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).OrderState(ctx, req.(*ServerOrderStateRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_SubscribeBatchAuction_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ChannelAuctioneerServer).SubscribeBatchAuction(&channelAuctioneerSubscribeBatchAuctionServer{stream})
}

type ChannelAuctioneer_SubscribeBatchAuctionServer interface {
	Send(*ServerAuctionMessage) error
	Recv() (*ClientAuctionMessage, error)
	grpc.ServerStream
}

type channelAuctioneerSubscribeBatchAuctionServer struct {
	grpc.ServerStream
}

func (x *channelAuctioneerSubscribeBatchAuctionServer) Send(m *ServerAuctionMessage) error {
	return x.ServerStream.SendMsg(m)
}

func (x *channelAuctioneerSubscribeBatchAuctionServer) Recv() (*ClientAuctionMessage, error) {
	m := new(ClientAuctionMessage)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _ChannelAuctioneer_SubscribeSidecar_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ChannelAuctioneerServer).SubscribeSidecar(&channelAuctioneerSubscribeSidecarServer{stream})
}

type ChannelAuctioneer_SubscribeSidecarServer interface {
	Send(*ServerAuctionMessage) error
	Recv() (*ClientAuctionMessage, error)
	grpc.ServerStream
}

type channelAuctioneerSubscribeSidecarServer struct {
	grpc.ServerStream
}

func (x *channelAuctioneerSubscribeSidecarServer) Send(m *ServerAuctionMessage) error {
	return x.ServerStream.SendMsg(m)
}

func (x *channelAuctioneerSubscribeSidecarServer) Recv() (*ClientAuctionMessage, error) {
	m := new(ClientAuctionMessage)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _ChannelAuctioneer_Terms_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(TermsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).Terms(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/Terms",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).Terms(ctx, req.(*TermsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_RelevantBatchSnapshot_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RelevantBatchRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).RelevantBatchSnapshot(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/RelevantBatchSnapshot",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).RelevantBatchSnapshot(ctx, req.(*RelevantBatchRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_BatchSnapshot_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(BatchSnapshotRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).BatchSnapshot(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/BatchSnapshot",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).BatchSnapshot(ctx, req.(*BatchSnapshotRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_NodeRating_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ServerNodeRatingRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).NodeRating(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/NodeRating",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).NodeRating(ctx, req.(*ServerNodeRatingRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_BatchSnapshots_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(BatchSnapshotsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).BatchSnapshots(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/BatchSnapshots",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).BatchSnapshots(ctx, req.(*BatchSnapshotsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ChannelAuctioneer_MarketInfo_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(MarketInfoRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ChannelAuctioneerServer).MarketInfo(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.ChannelAuctioneer/MarketInfo",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ChannelAuctioneerServer).MarketInfo(ctx, req.(*MarketInfoRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// ChannelAuctioneer_ServiceDesc is the grpc.ServiceDesc for ChannelAuctioneer service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var ChannelAuctioneer_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "poolrpc.ChannelAuctioneer",
	HandlerType: (*ChannelAuctioneerServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "ReserveAccount",
			Handler:    _ChannelAuctioneer_ReserveAccount_Handler,
		},
		{
			MethodName: "InitAccount",
			Handler:    _ChannelAuctioneer_InitAccount_Handler,
		},
		{
			MethodName: "ModifyAccount",
			Handler:    _ChannelAuctioneer_ModifyAccount_Handler,
		},
		{
			MethodName: "SubmitOrder",
			Handler:    _ChannelAuctioneer_SubmitOrder_Handler,
		},
		{
			MethodName: "CancelOrder",
			Handler:    _ChannelAuctioneer_CancelOrder_Handler,
		},
		{
			MethodName: "OrderState",
			Handler:    _ChannelAuctioneer_OrderState_Handler,
		},
		{
			MethodName: "Terms",
			Handler:    _ChannelAuctioneer_Terms_Handler,
		},
		{
			MethodName: "RelevantBatchSnapshot",
			Handler:    _ChannelAuctioneer_RelevantBatchSnapshot_Handler,
		},
		{
			MethodName: "BatchSnapshot",
			Handler:    _ChannelAuctioneer_BatchSnapshot_Handler,
		},
		{
			MethodName: "NodeRating",
			Handler:    _ChannelAuctioneer_NodeRating_Handler,
		},
		{
			MethodName: "BatchSnapshots",
			Handler:    _ChannelAuctioneer_BatchSnapshots_Handler,
		},
		{
			MethodName: "MarketInfo",
			Handler:    _ChannelAuctioneer_MarketInfo_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "SubscribeBatchAuction",
			Handler:       _ChannelAuctioneer_SubscribeBatchAuction_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
		{
			StreamName:    "SubscribeSidecar",
			Handler:       _ChannelAuctioneer_SubscribeSidecar_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "auctioneer.proto",
}
