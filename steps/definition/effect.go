package definition

// Body families: what kind of text or data a message body is. A step's Effect
// uses them to say what body it needs and what body it makes, so that a static
// check of a flow can tell, before anything runs, that a step gets a body it
// cannot take. "" is an unknown family.
const (
	FamilyJSON   = "json"   // a JSON text, or a decoded JSON value
	FamilyXML    = "xml"    // an XML document: one root element and no text outside it
	FamilyCSV    = "csv"    // comma (or otherwise) separated values
	FamilyText   = "text"   // plain text; any text is
	FamilyBinary = "binary" // bytes that are no text, such as a zip archive
)

// Header effects: what a step does to the headers it does not name in Sets or
// MaySet.
const (
	HeadersUnknown = ""       // anything may happen to them; nothing is known after the step
	HeadersKeep    = "keep"   // they stay as they are, and no other header appears
	HeadersAdd     = "add"    // they stay, and other headers may appear
	HeadersRemove  = "remove" // some of them may be removed; none appears
)

// BodyType is what is known about a message body; "" where nothing is.
type BodyType struct {
	Family string // FamilyJSON, FamilyXML, ...
	Media  string // the media type of its Content-Type header, such as application/json
	Format string // the name of its format (dil.core.formats)
}

// Effect is what a step is known to do to a message, for the static checks of
// a flow (dif validate). It is data: the step's implementation must guarantee
// what it declares, and where it cannot, it leaves that part unknown. A step
// without an Effect is unknown: after it nothing is known about the message.
//
// The zero Effect declares a step that keeps nothing: the body and the headers
// are unknown after it. KeepBody and Headers keep what is known.
type Effect struct {
	// Requires is the body the step needs. A known body of another family fails
	// the message there, unless Tolerant: then the step only gives a useless
	// result. Requires.Media is set only by a step that checks the Content-Type
	// header (and fails on another media type), Requires.Format by a step that
	// expects a named format.
	Requires BodyType
	Tolerant bool

	// Reads are the headers the step reads, each reference as the names it may
	// read (${header.a.b} reads a.b, else a). Expressions in the options and in
	// the link conditions of a router count.
	Reads [][]string

	// KeepBody: the body stays as it is (its family, format and media type). Else
	// Body is what it is after the step; Body.Media is the Content-Type the step
	// sets.
	KeepBody bool
	Body     BodyType

	// Headers is what happens to the headers (HeadersKeep, ...); then the step
	// sets the headers in Sets, and those in MaySet may or may not be set (or
	// removed) after it. Header names are not told apart by case.
	Headers string
	Sets    []string
	MaySet  []string

	// Links holds, for a router, the effect on its outbound links, in the order
	// of the node's links; a nil entry, or one past the end, is this Effect.
	Links []*Effect

	// Stops: the message never goes on after the step (wastebin).
	Stops bool

	// Otherwise: the step routes by the conditions of its links and stops a
	// message none of them holds for, unless a link has no condition (content).
	Otherwise bool

	// Reply: the step may choose the reply of the exchange, or end the message
	// before the end of the flow (return, setoneway, routers).
	Reply bool

	// ReplyNeeds is, for a source, the body the reply of the flow must have, as
	// a service contract checks it; "" where there is no such check.
	ReplyNeeds BodyType
}
