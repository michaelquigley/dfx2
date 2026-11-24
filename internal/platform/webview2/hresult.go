package webview2

import "fmt"

// hresult is a COM HRESULT, printed as Windows tools print it.
type hresult uint32

const (
	sOK          hresult = 0
	sFalse       hresult = 1
	eNoInterface hresult = 0x80004002
	ePointer     hresult = 0x80004003
	eAbort       hresult = 0x80004004
	eFail        hresult = 0x80004005
	eNotImpl     hresult = 0x80004001
	eOutOfMemory hresult = 0x8007000E
	eInvalidArg  hresult = 0x80070057
	// rpcChangedMode: CoInitializeEx found the thread in another apartment.
	rpcChangedMode hresult = 0x80010106
)

func (h hresult) failed() bool { return int32(h) < 0 }

func (h hresult) String() string {
	switch h {
	case sOK:
		return "S_OK"
	case sFalse:
		return "S_FALSE"
	case eAbort:
		return "E_ABORT (0x80004004)"
	case eFail:
		return "E_FAIL (0x80004005)"
	case 0x80070002:
		return "ERROR_FILE_NOT_FOUND (0x80070002)"
	case 0x8007139F:
		return "ERROR_INVALID_STATE (0x8007139F)"
	case rpcChangedMode:
		return "RPC_E_CHANGED_MODE (0x80010106)"
	}
	return fmt.Sprintf("0x%08X", uint32(h))
}

func (h hresult) err(what string) error {
	if !h.failed() {
		return nil
	}
	return fmt.Errorf("%s: HRESULT %v", what, h)
}
