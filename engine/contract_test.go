package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type fnAction func(context.Context, message.Message) (message.Message, error)

func (f fnAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

func TestFlowContractEntryAndOutputGates(t *testing.T) {
	var sawWire bool
	contract := &flowdef.Contract{
		Name:            "PlaceOrder",
		ExchangePattern: message.InOut,
		ValidateInput: func(_ context.Context, m message.Message, wire bool) error {
			sawWire = wire
			if m[message.Body] != "good-in" {
				return errors.New("bad input")
			}
			m["in.default"] = "ok"
			return nil
		},
		ValidateOutput: func(_ context.Context, m message.Message) error {
			if m[message.Body] != "good-out" {
				return errors.New("bad output")
			}
			m[message.ContentType] = "text/plain"
			return nil
		},
		ValidateError: func(_ context.Context, m message.Message) error {
			if m[message.Body] != `{"code":"E1"}` {
				return errors.New("bad error body")
			}
			m[message.ContentType] = "application/json"
			return nil
		},
	}

	makeFlow := func(act fnAction, handler *flowdef.ErrorHandler) *flowdef.Flow {
		b := &flowdef.Node{ID: "b", Kind: flowdef.Action, URI: "test:act", Processor: act}
		a := &flowdef.Node{ID: "a", Kind: flowdef.Source, URI: "service:PlaceOrder", Next: []*flowdef.Node{b}}
		f := &flowdef.Flow{ID: "f1", Source: a, Contract: contract}
		if handler != nil {
			f.Handlers = []*flowdef.ErrorHandler{handler}
		}
		return f
	}

	// 1. Valid request & reply stamps Content-Type and strips metadata.wireheaders.
	r := NewRunner(makeFlow(func(_ context.Context, m message.Message) (message.Message, error) {
		if _, leaked := m[message.WireHeaders]; leaked {
			t.Error("metadata.wireheaders leaked into flow")
		}
		if m["in.default"] != "ok" {
			t.Errorf("missing input default: %#v", m)
		}
		m[message.Body] = "good-out"
		return m, nil
	}, nil), nil)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	req := message.New("good-in")
	req[message.WireHeaders] = true
	reply, err := r.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("Request valid: %v", err)
	}
	if !sawWire || reply[message.Body] != "good-out" || reply[message.ContentType] != "text/plain" {
		t.Fatalf("unexpected reply: sawWire=%v reply=%#v", sawWire, reply)
	}

	// 2. Unhandled input contract violation returns *flowdef.ContractError{Kind: "input"}.
	_, err = r.Request(context.Background(), message.New("bad-in"))
	var ce *flowdef.ContractError
	if !errors.As(err, &ce) || ce.Kind != "input" || ce.Service != "PlaceOrder" {
		t.Fatalf("expected input ContractError, got %T (%v)", err, err)
	}

	// 3. Input contract violation matched by errorClass *ContractError turning into structured fault.
	errRoute := &flowdef.Node{ID: "eh-ret", Kind: flowdef.Action, Processor: fnAction(func(ctx context.Context, m message.Message) (message.Message, error) {
		fault := m.Copy()
		fault[message.Body] = `{"code":"E1"}`
		stepdef.Return(ctx, fault, errors.New("problem"), false)
		return m, nil
	})}
	var matchedClass string
	rHandled := NewRunner(makeFlow(func(_ context.Context, m message.Message) (message.Message, error) {
		return m, nil
	}, &flowdef.ErrorHandler{
		ID:    "eh",
		Route: errRoute,
		Match: func(f flowdef.Failure) bool {
			matchedClass = f.Class
			return strings.HasSuffix(f.Class, "ContractError")
		},
	}), nil)
	if err := rHandled.Start(); err != nil {
		t.Fatal(err)
	}
	defer rHandled.Stop()

	_, err = rHandled.Request(context.Background(), message.New("bad-in"))
	var fe *flowdef.FaultError
	if !errors.As(err, &fe) || !fe.Contracted || fe.Message[message.ContentType] != "application/json" || fe.Message[message.Body] != `{"code":"E1"}` {
		t.Fatalf("expected contracted FaultError, got %T (%v), class=%q", err, err, matchedClass)
	}

	// 4. Output contract breach fails the exchange and increments Failed count.
	rBadOut := NewRunner(makeFlow(func(_ context.Context, m message.Message) (message.Message, error) {
		m[message.Body] = "wrong-out"
		return m, nil
	}, nil), nil)
	if err := rBadOut.Start(); err != nil {
		t.Fatal(err)
	}
	defer rBadOut.Stop()

	_, err = rBadOut.Request(context.Background(), message.New("good-in"))
	if !errors.As(err, &ce) || ce.Kind != "output" {
		t.Fatalf("expected output ContractError, got %T (%v)", err, err)
	}
	if rBadOut.Status().Failed != 1 {
		t.Fatalf("expected Failed=1, got %+v", rBadOut.Status())
	}

	// 5. Fault contract breach fails with Kind == "error".
	rBadFault := NewRunner(makeFlow(func(ctx context.Context, m message.Message) (message.Message, error) {
		fault := m.Copy()
		fault[message.Body] = `{"wrong":true}`
		stepdef.Return(ctx, fault, errors.New("oops"), true)
		return m, nil
	}, nil), nil)
	if err := rBadFault.Start(); err != nil {
		t.Fatal(err)
	}
	defer rBadFault.Stop()

	_, err = rBadFault.Request(context.Background(), message.New("good-in"))
	if !errors.As(err, &ce) || ce.Kind != "error" {
		t.Fatalf("expected error ContractError, got %T (%v)", err, err)
	}
}

func TestFaultErrorPopulatesCallerErrorRouteHeaders(t *testing.T) {
	var captured message.Message
	caller := &flowdef.Flow{
		ID: "caller",
		Source: &flowdef.Node{
			ID:   "s",
			Kind: flowdef.Source,
			Next: []*flowdef.Node{{
				ID:   "call-svc",
				Kind: flowdef.Action,
				URI:  "service:PlaceOrder",
				Processor: fnAction(func(_ context.Context, _ message.Message) (message.Message, error) {
					return nil, &flowdef.FaultError{
						Service:    "PlaceOrder",
						Reason:     "out of stock",
						Message:    message.Message{message.Body: `{"code":"OOS"}`, message.ContentType: "application/json"},
						Contracted: true,
					}
				}),
			}},
		},
		Error: &flowdef.ErrorHandler{
			ID: "eh",
			Route: &flowdef.Node{
				ID:   "recover",
				Kind: flowdef.Action,
				Processor: fnAction(func(_ context.Context, m message.Message) (message.Message, error) {
					captured = m.Copy()
					return m, nil
				}),
			},
		},
	}

	if _, err := Run(context.Background(), caller, message.New("req")); err != nil {
		t.Fatalf("Run caller: %v", err)
	}
	if captured[ErrorBody] != `{"code":"OOS"}` || captured[ErrorContentType] != "application/json" || captured[ErrorMessage] != "out of stock" {
		t.Fatalf("unexpected error headers: %#v", captured)
	}
	if !strings.HasSuffix(captured[ErrorClass].(string), "FaultError") {
		t.Fatalf("expected FaultError class, got %q", captured[ErrorClass])
	}
}
