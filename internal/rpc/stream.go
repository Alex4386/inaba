package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// A Stream is a server-streaming procedure served at a GET path. The REST mapping in the
// protos takes server streams only as raw bytes, so these are listed here instead. The
// request is the path's {wildcards} and the query, by proto field name; the messages come
// as newline-delimited JSON, or as server-sent events to a client that accepts
// text/event-stream.
type Stream struct {
	// A ServeMux pattern, such as "GET /v1/threads/{thread_id}/events/watch".
	Pattern   string
	Procedure string
	// Request returns an empty request message.
	Request func() proto.Message
}

var wildcard = regexp.MustCompile(`\{([a-z_]+)\}`)

func serveStream(server *connect.Server, s Stream) http.Handler {
	names := wildcard.FindAllStringSubmatch(s.Pattern, -1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
		req := s.Request()
		values := map[string][]string{}
		for k, v := range r.URL.Query() {
			values[k] = v
		}
		for _, n := range names {
			values[n[1]] = []string{r.PathValue(n[1])}
		}
		if err := fill(req, values); err != nil {
			writeError(w, connect.NewError(connect.CodeInvalidArgument, err.Error()))
			return
		}
		info := &connect.CallInfo{
			Spec:     connect.Spec{Procedure: s.Procedure, StreamType: connect.StreamTypeServer},
			PeerAddr: r.RemoteAddr,
			Protocol: "rest",
			Codec:    "json",
		}
		for k, v := range r.Header {
			info.RequestHeader().SetValues(k, v)
		}
		st := &restStream{w: w, req: req, events: events}
		err := server.Call(r.Context(), s.Procedure, info, st)
		switch {
		case !st.started && err != nil:
			writeError(w, err)
		case !st.started:
			// A stream that ended without a message still answers.
			_ = st.SendHeaders()
			st.end(nil)
		default:
			st.end(err)
		}
	})
}

// fill sets a request's fields from string values, by proto field name or JSON name.
func fill(msg proto.Message, values map[string][]string) error {
	m := msg.ProtoReflect()
	fields := m.Descriptor().Fields()
	for name, vs := range values {
		f := fields.ByName(protoreflect.Name(name))
		if f == nil {
			f = fields.ByJSONName(name)
		}
		if f == nil || f.IsList() || f.IsMap() || len(vs) == 0 {
			return fmt.Errorf("no field %q to set from the URL", name)
		}
		v, err := scalar(f, vs[len(vs)-1])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		m.Set(f, v)
	}
	return nil
}

func scalar(f protoreflect.FieldDescriptor, s string) (protoreflect.Value, error) {
	switch f.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(s)
		return protoreflect.ValueOfBool(b), err
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(s, 10, 32)
		return protoreflect.ValueOfUint32(uint32(n)), err
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(s, 10, 64)
		return protoreflect.ValueOfUint64(n), err
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(s, 10, 32)
		return protoreflect.ValueOfInt32(int32(n)), err
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(s, 10, 64)
		return protoreflect.ValueOfInt64(n), err
	}
	return protoreflect.Value{}, errors.New("cannot be set from the URL")
}

// restStream is the server side of a REST server stream: the request from the URL, and
// each message written and flushed as it is sent.
type restStream struct {
	w        http.ResponseWriter
	req      proto.Message
	events   bool
	received bool
	started  bool
}

func (s *restStream) Receive(msg any) error {
	if s.received {
		return io.EOF
	}
	s.received = true
	m, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("rpc: Receive expects a proto.Message, got %T", msg)
	}
	proto.Merge(m, s.req)
	return nil
}

func (s *restStream) SendHeaders() error {
	if s.started {
		return nil
	}
	s.started = true
	h := s.w.Header()
	if s.events {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		// Proxies such as nginx otherwise hold events back.
		h.Set("X-Accel-Buffering", "no")
	} else {
		h.Set("Content-Type", "application/x-ndjson")
	}
	s.w.WriteHeader(http.StatusOK)
	return http.NewResponseController(s.w).Flush()
}

func (s *restStream) Send(msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("rpc: Send expects a proto.Message, got %T", msg)
	}
	data, err := protojson.Marshal(m)
	if err != nil {
		return err
	}
	if err := s.SendHeaders(); err != nil {
		return err
	}
	return s.write("", data)
}

// write sends one message: an event, or a line of JSON.
func (s *restStream) write(event string, data []byte) error {
	var b bytes.Buffer
	if s.events {
		if event != "" {
			b.WriteString("event: " + event + "\n")
		}
		// protojson output is one line, so it is one data line.
		b.WriteString("data: ")
		b.Write(bytes.ReplaceAll(data, []byte("\n"), nil))
		b.WriteString("\n\n")
	} else {
		b.Write(data)
		b.WriteByte('\n')
	}
	if _, err := s.w.Write(b.Bytes()); err != nil {
		return err
	}
	return http.NewResponseController(s.w).Flush()
}

// end closes a started stream. Over SSE a clean end sends an "end" event, so clients know
// not to reconnect, and a failure an "error" event; as JSON lines a failure is a last line
// holding {"error": ...}.
func (s *restStream) end(err error) {
	if err != nil && errors.Is(err, context.Canceled) {
		return
	}
	body, _ := json.Marshal(errorBody(err))
	switch {
	case s.events && err == nil:
		_ = s.write("end", []byte("{}"))
	case s.events:
		_ = s.write("error", body)
	case err != nil:
		line, _ := json.Marshal(map[string]any{"error": errorBody(err)})
		_ = s.write("", line)
	}
}

func errorBody(err error) map[string]string {
	if err == nil {
		return nil
	}
	return map[string]string{"code": connect.CodeOf(err).String(), "message": connectMessage(err)}
}

func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return err.Error()
}

// writeError answers a stream that never started with the error's status and JSON body.
func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus(connect.CodeOf(err)))
	_ = json.NewEncoder(w).Encode(errorBody(err))
}

func httpStatus(code connect.Code) int {
	switch code {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeCanceled:
		return 499
	}
	return http.StatusInternalServerError
}
