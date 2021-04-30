// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.23.0
// 	protoc        v3.6.1
// source: hashmail.proto

// We can't rename this to auctioneerrpc, otherwise it would be a breaking
// change since the package name is also contained in the HTTP URIs and old
// clients would call the wrong endpoints. Luckily with the go_package option we
// can have different golang and RPC package names.

package auctioneerrpc

import (
	context "context"
	proto "github.com/golang/protobuf/proto"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// This is a compile-time assertion that a sufficiently up-to-date version
// of the legacy proto package is being used.
const _ = proto.ProtoPackageIsVersion4

type PoolAccountAuth struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// The account key being used to authenticate.
	AcctKey []byte `protobuf:"bytes,1,opt,name=acct_key,json=acctKey,proto3" json:"acct_key,omitempty"`
	// A valid signature over the stream ID being used.
	StreamSig []byte `protobuf:"bytes,2,opt,name=stream_sig,json=streamSig,proto3" json:"stream_sig,omitempty"`
}

func (x *PoolAccountAuth) Reset() {
	*x = PoolAccountAuth{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PoolAccountAuth) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PoolAccountAuth) ProtoMessage() {}

func (x *PoolAccountAuth) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PoolAccountAuth.ProtoReflect.Descriptor instead.
func (*PoolAccountAuth) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{0}
}

func (x *PoolAccountAuth) GetAcctKey() []byte {
	if x != nil {
		return x.AcctKey
	}
	return nil
}

func (x *PoolAccountAuth) GetStreamSig() []byte {
	if x != nil {
		return x.StreamSig
	}
	return nil
}

type SidecarAuth struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	//
	//A valid sidecar ticket that has been signed (offered) by a Pool account in
	//the active state.
	Ticket string `protobuf:"bytes,1,opt,name=ticket,proto3" json:"ticket,omitempty"`
}

func (x *SidecarAuth) Reset() {
	*x = SidecarAuth{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *SidecarAuth) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SidecarAuth) ProtoMessage() {}

func (x *SidecarAuth) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SidecarAuth.ProtoReflect.Descriptor instead.
func (*SidecarAuth) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{1}
}

func (x *SidecarAuth) GetTicket() string {
	if x != nil {
		return x.Ticket
	}
	return ""
}

type CipherBoxAuth struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// A description of the stream one is attempting to initialize.
	Desc *CipherBoxDesc `protobuf:"bytes,1,opt,name=desc,proto3" json:"desc,omitempty"`
	// Types that are assignable to Auth:
	//	*CipherBoxAuth_AcctAuth
	//	*CipherBoxAuth_SidecarAuth
	Auth isCipherBoxAuth_Auth `protobuf_oneof:"auth"`
}

func (x *CipherBoxAuth) Reset() {
	*x = CipherBoxAuth{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherBoxAuth) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherBoxAuth) ProtoMessage() {}

func (x *CipherBoxAuth) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherBoxAuth.ProtoReflect.Descriptor instead.
func (*CipherBoxAuth) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{2}
}

func (x *CipherBoxAuth) GetDesc() *CipherBoxDesc {
	if x != nil {
		return x.Desc
	}
	return nil
}

func (m *CipherBoxAuth) GetAuth() isCipherBoxAuth_Auth {
	if m != nil {
		return m.Auth
	}
	return nil
}

func (x *CipherBoxAuth) GetAcctAuth() *PoolAccountAuth {
	if x, ok := x.GetAuth().(*CipherBoxAuth_AcctAuth); ok {
		return x.AcctAuth
	}
	return nil
}

func (x *CipherBoxAuth) GetSidecarAuth() *SidecarAuth {
	if x, ok := x.GetAuth().(*CipherBoxAuth_SidecarAuth); ok {
		return x.SidecarAuth
	}
	return nil
}

type isCipherBoxAuth_Auth interface {
	isCipherBoxAuth_Auth()
}

type CipherBoxAuth_AcctAuth struct {
	AcctAuth *PoolAccountAuth `protobuf:"bytes,2,opt,name=acct_auth,json=acctAuth,proto3,oneof"`
}

type CipherBoxAuth_SidecarAuth struct {
	SidecarAuth *SidecarAuth `protobuf:"bytes,3,opt,name=sidecar_auth,json=sidecarAuth,proto3,oneof"`
}

func (*CipherBoxAuth_AcctAuth) isCipherBoxAuth_Auth() {}

func (*CipherBoxAuth_SidecarAuth) isCipherBoxAuth_Auth() {}

type DelCipherBoxResp struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *DelCipherBoxResp) Reset() {
	*x = DelCipherBoxResp{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *DelCipherBoxResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DelCipherBoxResp) ProtoMessage() {}

func (x *DelCipherBoxResp) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DelCipherBoxResp.ProtoReflect.Descriptor instead.
func (*DelCipherBoxResp) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{3}
}

type CipherChallenge struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *CipherChallenge) Reset() {
	*x = CipherChallenge{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherChallenge) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherChallenge) ProtoMessage() {}

func (x *CipherChallenge) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherChallenge.ProtoReflect.Descriptor instead.
func (*CipherChallenge) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{4}
}

type CipherError struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *CipherError) Reset() {
	*x = CipherError{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherError) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherError) ProtoMessage() {}

func (x *CipherError) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherError.ProtoReflect.Descriptor instead.
func (*CipherError) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{5}
}

type CipherSuccess struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Desc *CipherBoxDesc `protobuf:"bytes,1,opt,name=desc,proto3" json:"desc,omitempty"`
}

func (x *CipherSuccess) Reset() {
	*x = CipherSuccess{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherSuccess) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherSuccess) ProtoMessage() {}

func (x *CipherSuccess) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherSuccess.ProtoReflect.Descriptor instead.
func (*CipherSuccess) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{6}
}

func (x *CipherSuccess) GetDesc() *CipherBoxDesc {
	if x != nil {
		return x.Desc
	}
	return nil
}

type CipherInitResp struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// Types that are assignable to Resp:
	//	*CipherInitResp_Success
	//	*CipherInitResp_Challenge
	//	*CipherInitResp_Error
	Resp isCipherInitResp_Resp `protobuf_oneof:"resp"`
}

func (x *CipherInitResp) Reset() {
	*x = CipherInitResp{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherInitResp) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherInitResp) ProtoMessage() {}

func (x *CipherInitResp) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherInitResp.ProtoReflect.Descriptor instead.
func (*CipherInitResp) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{7}
}

func (m *CipherInitResp) GetResp() isCipherInitResp_Resp {
	if m != nil {
		return m.Resp
	}
	return nil
}

func (x *CipherInitResp) GetSuccess() *CipherSuccess {
	if x, ok := x.GetResp().(*CipherInitResp_Success); ok {
		return x.Success
	}
	return nil
}

func (x *CipherInitResp) GetChallenge() *CipherChallenge {
	if x, ok := x.GetResp().(*CipherInitResp_Challenge); ok {
		return x.Challenge
	}
	return nil
}

func (x *CipherInitResp) GetError() *CipherError {
	if x, ok := x.GetResp().(*CipherInitResp_Error); ok {
		return x.Error
	}
	return nil
}

type isCipherInitResp_Resp interface {
	isCipherInitResp_Resp()
}

type CipherInitResp_Success struct {
	//
	//CipherSuccess is returned if the initialization of the cipher box was
	//successful.
	Success *CipherSuccess `protobuf:"bytes,1,opt,name=success,proto3,oneof"`
}

type CipherInitResp_Challenge struct {
	//
	//CipherChallenge is returned if the authentication mechanism was revoked
	//or needs to be refreshed.
	Challenge *CipherChallenge `protobuf:"bytes,2,opt,name=challenge,proto3,oneof"`
}

type CipherInitResp_Error struct {
	//
	//CipherError is returned if the authentication mechanism failed to
	//validate.
	Error *CipherError `protobuf:"bytes,3,opt,name=error,proto3,oneof"`
}

func (*CipherInitResp_Success) isCipherInitResp_Resp() {}

func (*CipherInitResp_Challenge) isCipherInitResp_Resp() {}

func (*CipherInitResp_Error) isCipherInitResp_Resp() {}

type CipherBoxDesc struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	StreamId []byte `protobuf:"bytes,1,opt,name=stream_id,json=streamId,proto3" json:"stream_id,omitempty"`
}

func (x *CipherBoxDesc) Reset() {
	*x = CipherBoxDesc{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[8]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherBoxDesc) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherBoxDesc) ProtoMessage() {}

func (x *CipherBoxDesc) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[8]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherBoxDesc.ProtoReflect.Descriptor instead.
func (*CipherBoxDesc) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{8}
}

func (x *CipherBoxDesc) GetStreamId() []byte {
	if x != nil {
		return x.StreamId
	}
	return nil
}

type CipherBox struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Desc *CipherBoxDesc `protobuf:"bytes,1,opt,name=desc,proto3" json:"desc,omitempty"`
	Msg  []byte         `protobuf:"bytes,2,opt,name=msg,proto3" json:"msg,omitempty"`
}

func (x *CipherBox) Reset() {
	*x = CipherBox{}
	if protoimpl.UnsafeEnabled {
		mi := &file_hashmail_proto_msgTypes[9]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *CipherBox) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CipherBox) ProtoMessage() {}

func (x *CipherBox) ProtoReflect() protoreflect.Message {
	mi := &file_hashmail_proto_msgTypes[9]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CipherBox.ProtoReflect.Descriptor instead.
func (*CipherBox) Descriptor() ([]byte, []int) {
	return file_hashmail_proto_rawDescGZIP(), []int{9}
}

func (x *CipherBox) GetDesc() *CipherBoxDesc {
	if x != nil {
		return x.Desc
	}
	return nil
}

func (x *CipherBox) GetMsg() []byte {
	if x != nil {
		return x.Msg
	}
	return nil
}

var File_hashmail_proto protoreflect.FileDescriptor

var file_hashmail_proto_rawDesc = []byte{
	0x0a, 0x0e, 0x68, 0x61, 0x73, 0x68, 0x6d, 0x61, 0x69, 0x6c, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x12, 0x07, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x22, 0x4b, 0x0a, 0x0f, 0x50, 0x6f, 0x6f,
	0x6c, 0x41, 0x63, 0x63, 0x6f, 0x75, 0x6e, 0x74, 0x41, 0x75, 0x74, 0x68, 0x12, 0x19, 0x0a, 0x08,
	0x61, 0x63, 0x63, 0x74, 0x5f, 0x6b, 0x65, 0x79, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x07,
	0x61, 0x63, 0x63, 0x74, 0x4b, 0x65, 0x79, 0x12, 0x1d, 0x0a, 0x0a, 0x73, 0x74, 0x72, 0x65, 0x61,
	0x6d, 0x5f, 0x73, 0x69, 0x67, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x09, 0x73, 0x74, 0x72,
	0x65, 0x61, 0x6d, 0x53, 0x69, 0x67, 0x22, 0x25, 0x0a, 0x0b, 0x53, 0x69, 0x64, 0x65, 0x63, 0x61,
	0x72, 0x41, 0x75, 0x74, 0x68, 0x12, 0x16, 0x0a, 0x06, 0x74, 0x69, 0x63, 0x6b, 0x65, 0x74, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x74, 0x69, 0x63, 0x6b, 0x65, 0x74, 0x22, 0xb7, 0x01,
	0x0a, 0x0d, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x41, 0x75, 0x74, 0x68, 0x12,
	0x2a, 0x0a, 0x04, 0x64, 0x65, 0x73, 0x63, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e,
	0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f,
	0x78, 0x44, 0x65, 0x73, 0x63, 0x52, 0x04, 0x64, 0x65, 0x73, 0x63, 0x12, 0x37, 0x0a, 0x09, 0x61,
	0x63, 0x63, 0x74, 0x5f, 0x61, 0x75, 0x74, 0x68, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x18,
	0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x50, 0x6f, 0x6f, 0x6c, 0x41, 0x63, 0x63,
	0x6f, 0x75, 0x6e, 0x74, 0x41, 0x75, 0x74, 0x68, 0x48, 0x00, 0x52, 0x08, 0x61, 0x63, 0x63, 0x74,
	0x41, 0x75, 0x74, 0x68, 0x12, 0x39, 0x0a, 0x0c, 0x73, 0x69, 0x64, 0x65, 0x63, 0x61, 0x72, 0x5f,
	0x61, 0x75, 0x74, 0x68, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x14, 0x2e, 0x70, 0x6f, 0x6f,
	0x6c, 0x72, 0x70, 0x63, 0x2e, 0x53, 0x69, 0x64, 0x65, 0x63, 0x61, 0x72, 0x41, 0x75, 0x74, 0x68,
	0x48, 0x00, 0x52, 0x0b, 0x73, 0x69, 0x64, 0x65, 0x63, 0x61, 0x72, 0x41, 0x75, 0x74, 0x68, 0x42,
	0x06, 0x0a, 0x04, 0x61, 0x75, 0x74, 0x68, 0x22, 0x12, 0x0a, 0x10, 0x44, 0x65, 0x6c, 0x43, 0x69,
	0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x52, 0x65, 0x73, 0x70, 0x22, 0x11, 0x0a, 0x0f, 0x43,
	0x69, 0x70, 0x68, 0x65, 0x72, 0x43, 0x68, 0x61, 0x6c, 0x6c, 0x65, 0x6e, 0x67, 0x65, 0x22, 0x0d,
	0x0a, 0x0b, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x22, 0x3b, 0x0a,
	0x0d, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x53, 0x75, 0x63, 0x63, 0x65, 0x73, 0x73, 0x12, 0x2a,
	0x0a, 0x04, 0x64, 0x65, 0x73, 0x63, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e, 0x70,
	0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78,
	0x44, 0x65, 0x73, 0x63, 0x52, 0x04, 0x64, 0x65, 0x73, 0x63, 0x22, 0xb4, 0x01, 0x0a, 0x0e, 0x43,
	0x69, 0x70, 0x68, 0x65, 0x72, 0x49, 0x6e, 0x69, 0x74, 0x52, 0x65, 0x73, 0x70, 0x12, 0x32, 0x0a,
	0x07, 0x73, 0x75, 0x63, 0x63, 0x65, 0x73, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16,
	0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x53,
	0x75, 0x63, 0x63, 0x65, 0x73, 0x73, 0x48, 0x00, 0x52, 0x07, 0x73, 0x75, 0x63, 0x63, 0x65, 0x73,
	0x73, 0x12, 0x38, 0x0a, 0x09, 0x63, 0x68, 0x61, 0x6c, 0x6c, 0x65, 0x6e, 0x67, 0x65, 0x18, 0x02,
	0x20, 0x01, 0x28, 0x0b, 0x32, 0x18, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43,
	0x69, 0x70, 0x68, 0x65, 0x72, 0x43, 0x68, 0x61, 0x6c, 0x6c, 0x65, 0x6e, 0x67, 0x65, 0x48, 0x00,
	0x52, 0x09, 0x63, 0x68, 0x61, 0x6c, 0x6c, 0x65, 0x6e, 0x67, 0x65, 0x12, 0x2c, 0x0a, 0x05, 0x65,
	0x72, 0x72, 0x6f, 0x72, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x14, 0x2e, 0x70, 0x6f, 0x6f,
	0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x45, 0x72, 0x72, 0x6f, 0x72,
	0x48, 0x00, 0x52, 0x05, 0x65, 0x72, 0x72, 0x6f, 0x72, 0x42, 0x06, 0x0a, 0x04, 0x72, 0x65, 0x73,
	0x70, 0x22, 0x2c, 0x0a, 0x0d, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x44, 0x65,
	0x73, 0x63, 0x12, 0x1b, 0x0a, 0x09, 0x73, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x5f, 0x69, 0x64, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x08, 0x73, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x49, 0x64, 0x22,
	0x49, 0x0a, 0x09, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x12, 0x2a, 0x0a, 0x04,
	0x64, 0x65, 0x73, 0x63, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e, 0x70, 0x6f, 0x6f,
	0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x44, 0x65,
	0x73, 0x63, 0x52, 0x04, 0x64, 0x65, 0x73, 0x63, 0x12, 0x10, 0x0a, 0x03, 0x6d, 0x73, 0x67, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x03, 0x6d, 0x73, 0x67, 0x32, 0x86, 0x02, 0x0a, 0x08, 0x48,
	0x61, 0x73, 0x68, 0x4d, 0x61, 0x69, 0x6c, 0x12, 0x3f, 0x0a, 0x0c, 0x4e, 0x65, 0x77, 0x43, 0x69,
	0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x12, 0x16, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70,
	0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x41, 0x75, 0x74, 0x68, 0x1a,
	0x17, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72,
	0x49, 0x6e, 0x69, 0x74, 0x52, 0x65, 0x73, 0x70, 0x12, 0x41, 0x0a, 0x0c, 0x44, 0x65, 0x6c, 0x43,
	0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x12, 0x16, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72,
	0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x41, 0x75, 0x74, 0x68,
	0x1a, 0x19, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x44, 0x65, 0x6c, 0x43, 0x69,
	0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x52, 0x65, 0x73, 0x70, 0x12, 0x3a, 0x0a, 0x0a, 0x53,
	0x65, 0x6e, 0x64, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x12, 0x12, 0x2e, 0x70, 0x6f, 0x6f, 0x6c,
	0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x1a, 0x16, 0x2e,
	0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f,
	0x78, 0x44, 0x65, 0x73, 0x63, 0x28, 0x01, 0x12, 0x3a, 0x0a, 0x0a, 0x52, 0x65, 0x63, 0x76, 0x53,
	0x74, 0x72, 0x65, 0x61, 0x6d, 0x12, 0x16, 0x2e, 0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e,
	0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f, 0x78, 0x44, 0x65, 0x73, 0x63, 0x1a, 0x12, 0x2e,
	0x70, 0x6f, 0x6f, 0x6c, 0x72, 0x70, 0x63, 0x2e, 0x43, 0x69, 0x70, 0x68, 0x65, 0x72, 0x42, 0x6f,
	0x78, 0x30, 0x01, 0x42, 0x2d, 0x5a, 0x2b, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f,
	0x6d, 0x2f, 0x6c, 0x69, 0x67, 0x68, 0x74, 0x6e, 0x69, 0x6e, 0x67, 0x6c, 0x61, 0x62, 0x73, 0x2f,
	0x70, 0x6f, 0x6f, 0x6c, 0x2f, 0x61, 0x75, 0x63, 0x74, 0x69, 0x6f, 0x6e, 0x65, 0x65, 0x72, 0x72,
	0x70, 0x63, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_hashmail_proto_rawDescOnce sync.Once
	file_hashmail_proto_rawDescData = file_hashmail_proto_rawDesc
)

func file_hashmail_proto_rawDescGZIP() []byte {
	file_hashmail_proto_rawDescOnce.Do(func() {
		file_hashmail_proto_rawDescData = protoimpl.X.CompressGZIP(file_hashmail_proto_rawDescData)
	})
	return file_hashmail_proto_rawDescData
}

var file_hashmail_proto_msgTypes = make([]protoimpl.MessageInfo, 10)
var file_hashmail_proto_goTypes = []interface{}{
	(*PoolAccountAuth)(nil),  // 0: poolrpc.PoolAccountAuth
	(*SidecarAuth)(nil),      // 1: poolrpc.SidecarAuth
	(*CipherBoxAuth)(nil),    // 2: poolrpc.CipherBoxAuth
	(*DelCipherBoxResp)(nil), // 3: poolrpc.DelCipherBoxResp
	(*CipherChallenge)(nil),  // 4: poolrpc.CipherChallenge
	(*CipherError)(nil),      // 5: poolrpc.CipherError
	(*CipherSuccess)(nil),    // 6: poolrpc.CipherSuccess
	(*CipherInitResp)(nil),   // 7: poolrpc.CipherInitResp
	(*CipherBoxDesc)(nil),    // 8: poolrpc.CipherBoxDesc
	(*CipherBox)(nil),        // 9: poolrpc.CipherBox
}
var file_hashmail_proto_depIdxs = []int32{
	8,  // 0: poolrpc.CipherBoxAuth.desc:type_name -> poolrpc.CipherBoxDesc
	0,  // 1: poolrpc.CipherBoxAuth.acct_auth:type_name -> poolrpc.PoolAccountAuth
	1,  // 2: poolrpc.CipherBoxAuth.sidecar_auth:type_name -> poolrpc.SidecarAuth
	8,  // 3: poolrpc.CipherSuccess.desc:type_name -> poolrpc.CipherBoxDesc
	6,  // 4: poolrpc.CipherInitResp.success:type_name -> poolrpc.CipherSuccess
	4,  // 5: poolrpc.CipherInitResp.challenge:type_name -> poolrpc.CipherChallenge
	5,  // 6: poolrpc.CipherInitResp.error:type_name -> poolrpc.CipherError
	8,  // 7: poolrpc.CipherBox.desc:type_name -> poolrpc.CipherBoxDesc
	2,  // 8: poolrpc.HashMail.NewCipherBox:input_type -> poolrpc.CipherBoxAuth
	2,  // 9: poolrpc.HashMail.DelCipherBox:input_type -> poolrpc.CipherBoxAuth
	9,  // 10: poolrpc.HashMail.SendStream:input_type -> poolrpc.CipherBox
	8,  // 11: poolrpc.HashMail.RecvStream:input_type -> poolrpc.CipherBoxDesc
	7,  // 12: poolrpc.HashMail.NewCipherBox:output_type -> poolrpc.CipherInitResp
	3,  // 13: poolrpc.HashMail.DelCipherBox:output_type -> poolrpc.DelCipherBoxResp
	8,  // 14: poolrpc.HashMail.SendStream:output_type -> poolrpc.CipherBoxDesc
	9,  // 15: poolrpc.HashMail.RecvStream:output_type -> poolrpc.CipherBox
	12, // [12:16] is the sub-list for method output_type
	8,  // [8:12] is the sub-list for method input_type
	8,  // [8:8] is the sub-list for extension type_name
	8,  // [8:8] is the sub-list for extension extendee
	0,  // [0:8] is the sub-list for field type_name
}

func init() { file_hashmail_proto_init() }
func file_hashmail_proto_init() {
	if File_hashmail_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_hashmail_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PoolAccountAuth); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*SidecarAuth); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherBoxAuth); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*DelCipherBoxResp); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherChallenge); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherError); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherSuccess); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[7].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherInitResp); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[8].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherBoxDesc); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_hashmail_proto_msgTypes[9].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*CipherBox); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	file_hashmail_proto_msgTypes[2].OneofWrappers = []interface{}{
		(*CipherBoxAuth_AcctAuth)(nil),
		(*CipherBoxAuth_SidecarAuth)(nil),
	}
	file_hashmail_proto_msgTypes[7].OneofWrappers = []interface{}{
		(*CipherInitResp_Success)(nil),
		(*CipherInitResp_Challenge)(nil),
		(*CipherInitResp_Error)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_hashmail_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   10,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_hashmail_proto_goTypes,
		DependencyIndexes: file_hashmail_proto_depIdxs,
		MessageInfos:      file_hashmail_proto_msgTypes,
	}.Build()
	File_hashmail_proto = out.File
	file_hashmail_proto_rawDesc = nil
	file_hashmail_proto_goTypes = nil
	file_hashmail_proto_depIdxs = nil
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConnInterface

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion6

// HashMailClient is the client API for HashMail service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type HashMailClient interface {
	//
	//NewCipherBox creates a new cipher box pipe/stream given a valid
	//authentication mechanism. If the authentication mechanism has been revoked,
	//or needs to be changed, then a CipherChallenge message is returned.
	//Otherwise the method will either be accepted or rejected.
	NewCipherBox(ctx context.Context, in *CipherBoxAuth, opts ...grpc.CallOption) (*CipherInitResp, error)
	//
	//DelCipherBox attempts to tear down an existing cipher box pipe. The same
	//authentication mechanism used to initially create the stream MUST be
	//specified.
	DelCipherBox(ctx context.Context, in *CipherBoxAuth, opts ...grpc.CallOption) (*DelCipherBoxResp, error)
	//
	//SendStream opens up the write side of the passed CipherBox pipe. Writes
	//will be non-blocking up to the buffer size of the pipe. Beyond that writes
	//will block until completed.
	SendStream(ctx context.Context, opts ...grpc.CallOption) (HashMail_SendStreamClient, error)
	//
	//RecvStream opens up the read side of the passed CipherBox pipe. This method
	//will block until a full message has been read as this is a message based
	//pipe/stream abstraction.
	RecvStream(ctx context.Context, in *CipherBoxDesc, opts ...grpc.CallOption) (HashMail_RecvStreamClient, error)
}

type hashMailClient struct {
	cc grpc.ClientConnInterface
}

func NewHashMailClient(cc grpc.ClientConnInterface) HashMailClient {
	return &hashMailClient{cc}
}

func (c *hashMailClient) NewCipherBox(ctx context.Context, in *CipherBoxAuth, opts ...grpc.CallOption) (*CipherInitResp, error) {
	out := new(CipherInitResp)
	err := c.cc.Invoke(ctx, "/poolrpc.HashMail/NewCipherBox", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *hashMailClient) DelCipherBox(ctx context.Context, in *CipherBoxAuth, opts ...grpc.CallOption) (*DelCipherBoxResp, error) {
	out := new(DelCipherBoxResp)
	err := c.cc.Invoke(ctx, "/poolrpc.HashMail/DelCipherBox", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *hashMailClient) SendStream(ctx context.Context, opts ...grpc.CallOption) (HashMail_SendStreamClient, error) {
	stream, err := c.cc.NewStream(ctx, &_HashMail_serviceDesc.Streams[0], "/poolrpc.HashMail/SendStream", opts...)
	if err != nil {
		return nil, err
	}
	x := &hashMailSendStreamClient{stream}
	return x, nil
}

type HashMail_SendStreamClient interface {
	Send(*CipherBox) error
	CloseAndRecv() (*CipherBoxDesc, error)
	grpc.ClientStream
}

type hashMailSendStreamClient struct {
	grpc.ClientStream
}

func (x *hashMailSendStreamClient) Send(m *CipherBox) error {
	return x.ClientStream.SendMsg(m)
}

func (x *hashMailSendStreamClient) CloseAndRecv() (*CipherBoxDesc, error) {
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	m := new(CipherBoxDesc)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *hashMailClient) RecvStream(ctx context.Context, in *CipherBoxDesc, opts ...grpc.CallOption) (HashMail_RecvStreamClient, error) {
	stream, err := c.cc.NewStream(ctx, &_HashMail_serviceDesc.Streams[1], "/poolrpc.HashMail/RecvStream", opts...)
	if err != nil {
		return nil, err
	}
	x := &hashMailRecvStreamClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type HashMail_RecvStreamClient interface {
	Recv() (*CipherBox, error)
	grpc.ClientStream
}

type hashMailRecvStreamClient struct {
	grpc.ClientStream
}

func (x *hashMailRecvStreamClient) Recv() (*CipherBox, error) {
	m := new(CipherBox)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// HashMailServer is the server API for HashMail service.
type HashMailServer interface {
	//
	//NewCipherBox creates a new cipher box pipe/stream given a valid
	//authentication mechanism. If the authentication mechanism has been revoked,
	//or needs to be changed, then a CipherChallenge message is returned.
	//Otherwise the method will either be accepted or rejected.
	NewCipherBox(context.Context, *CipherBoxAuth) (*CipherInitResp, error)
	//
	//DelCipherBox attempts to tear down an existing cipher box pipe. The same
	//authentication mechanism used to initially create the stream MUST be
	//specified.
	DelCipherBox(context.Context, *CipherBoxAuth) (*DelCipherBoxResp, error)
	//
	//SendStream opens up the write side of the passed CipherBox pipe. Writes
	//will be non-blocking up to the buffer size of the pipe. Beyond that writes
	//will block until completed.
	SendStream(HashMail_SendStreamServer) error
	//
	//RecvStream opens up the read side of the passed CipherBox pipe. This method
	//will block until a full message has been read as this is a message based
	//pipe/stream abstraction.
	RecvStream(*CipherBoxDesc, HashMail_RecvStreamServer) error
}

// UnimplementedHashMailServer can be embedded to have forward compatible implementations.
type UnimplementedHashMailServer struct {
}

func (*UnimplementedHashMailServer) NewCipherBox(context.Context, *CipherBoxAuth) (*CipherInitResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method NewCipherBox not implemented")
}
func (*UnimplementedHashMailServer) DelCipherBox(context.Context, *CipherBoxAuth) (*DelCipherBoxResp, error) {
	return nil, status.Errorf(codes.Unimplemented, "method DelCipherBox not implemented")
}
func (*UnimplementedHashMailServer) SendStream(HashMail_SendStreamServer) error {
	return status.Errorf(codes.Unimplemented, "method SendStream not implemented")
}
func (*UnimplementedHashMailServer) RecvStream(*CipherBoxDesc, HashMail_RecvStreamServer) error {
	return status.Errorf(codes.Unimplemented, "method RecvStream not implemented")
}

func RegisterHashMailServer(s *grpc.Server, srv HashMailServer) {
	s.RegisterService(&_HashMail_serviceDesc, srv)
}

func _HashMail_NewCipherBox_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CipherBoxAuth)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(HashMailServer).NewCipherBox(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.HashMail/NewCipherBox",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(HashMailServer).NewCipherBox(ctx, req.(*CipherBoxAuth))
	}
	return interceptor(ctx, in, info, handler)
}

func _HashMail_DelCipherBox_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CipherBoxAuth)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(HashMailServer).DelCipherBox(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/poolrpc.HashMail/DelCipherBox",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(HashMailServer).DelCipherBox(ctx, req.(*CipherBoxAuth))
	}
	return interceptor(ctx, in, info, handler)
}

func _HashMail_SendStream_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(HashMailServer).SendStream(&hashMailSendStreamServer{stream})
}

type HashMail_SendStreamServer interface {
	SendAndClose(*CipherBoxDesc) error
	Recv() (*CipherBox, error)
	grpc.ServerStream
}

type hashMailSendStreamServer struct {
	grpc.ServerStream
}

func (x *hashMailSendStreamServer) SendAndClose(m *CipherBoxDesc) error {
	return x.ServerStream.SendMsg(m)
}

func (x *hashMailSendStreamServer) Recv() (*CipherBox, error) {
	m := new(CipherBox)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _HashMail_RecvStream_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(CipherBoxDesc)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(HashMailServer).RecvStream(m, &hashMailRecvStreamServer{stream})
}

type HashMail_RecvStreamServer interface {
	Send(*CipherBox) error
	grpc.ServerStream
}

type hashMailRecvStreamServer struct {
	grpc.ServerStream
}

func (x *hashMailRecvStreamServer) Send(m *CipherBox) error {
	return x.ServerStream.SendMsg(m)
}

var _HashMail_serviceDesc = grpc.ServiceDesc{
	ServiceName: "poolrpc.HashMail",
	HandlerType: (*HashMailServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "NewCipherBox",
			Handler:    _HashMail_NewCipherBox_Handler,
		},
		{
			MethodName: "DelCipherBox",
			Handler:    _HashMail_DelCipherBox_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "SendStream",
			Handler:       _HashMail_SendStream_Handler,
			ClientStreams: true,
		},
		{
			StreamName:    "RecvStream",
			Handler:       _HashMail_RecvStream_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "hashmail.proto",
}
