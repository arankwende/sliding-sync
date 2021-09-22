package synclive

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/rs/zerolog/hlog"
	"github.com/tidwall/gjson"
)

type SyncLiveHandler struct {
	V2        sync2.Client
	Storage   *state.Storage
	V2Store   *sync2.Storage
	PollerMap *sync2.PollerMap
	Notifier  *Notifier
}

func NewSyncLiveHandler(v2Client sync2.Client, postgresDBURI string) (*SyncLiveHandler, error) {
	sh := &SyncLiveHandler{
		V2:      v2Client,
		Storage: state.NewStorage(postgresDBURI),
		V2Store: sync2.NewStore(postgresDBURI),
	}
	sh.PollerMap = sync2.NewPollerMap(v2Client, sh)
	sh.Notifier = NewNotifier(sh.Storage)

	roomToJoinedUsers, err := sh.Storage.AllJoinedMembers()
	if err != nil {
		return nil, err
	}
	sh.Notifier.LoadJoinedUsers(roomToJoinedUsers)

	return sh, nil
}

func (h *SyncLiveHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	err := h.serve(w, req)
	if err != nil {
		herr, ok := err.(*internal.HandlerError)
		if !ok {
			herr = &internal.HandlerError{
				StatusCode: 500,
				Err:        err,
			}
		}
		w.WriteHeader(herr.StatusCode)
		w.Write(herr.JSON())
	}
}

// Entry point for sync v3
func (h *SyncLiveHandler) serve(w http.ResponseWriter, req *http.Request) error {
	conn, err := h.setupConnection(req)
	if err != nil {
		hlog.FromRequest(req).Err(err).Msg("failed to get or create Conn")
		return err
	}
	log := hlog.FromRequest(req).With().Str("conn_id", conn.ConnID.String()).Logger()
	var cpos int64
	queryPos := req.URL.Query().Get("pos")
	if queryPos != "" {
		cpos, err = strconv.ParseInt(queryPos, 10, 64)
		if err != nil {
			log.Err(err).Msg("failed to get ?pos=")
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        fmt.Errorf("invalid position: %s", queryPos),
			}
		}
	}
	var body []byte
	if req.Body != nil {
		defer req.Body.Close()
		body, err = ioutil.ReadAll(req.Body)
		if err != nil {
			log.Err(err).Msg("failed to read request body")
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
	}
	nextPos, nextData, herr := conn.OnIncomingRequest(req.Context(), cpos, body)
	if herr != nil {
		log.Err(herr).Msg("failed to OnIncomingRequest")
		return herr
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Sync3-Position", fmt.Sprintf("%d", nextPos))
	w.Header().Set("X-Sync3-Session", conn.ConnID.SessionID)
	w.WriteHeader(200)
	w.Write(nextData)
	return nil
}

// setupConnection associates this request with an existing connection or makes a new connection.
// It also sets a v2 sync poll loop going if one didn't exist already for this user.
// When this function returns, the connection is alive and active.
func (h *SyncLiveHandler) setupConnection(req *http.Request) (*Conn, error) {
	log := hlog.FromRequest(req)
	var conn *Conn

	// Identify the device
	deviceID, err := internal.DeviceIDFromRequest(req)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get device ID from request")
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        err,
		}
	}
	sessionID := req.URL.Query().Get("session")

	// client thinks they have a connection
	if sessionID != "" {
		// Lookup the connection
		// we need to map based on both as the session ID isn't crypto secure but the device ID is (Auth header)
		conn = h.Notifier.Conn(ConnID{
			SessionID: sessionID,
			DeviceID:  deviceID,
		})
		if err != nil {
			log.Warn().Err(err).Msg("failed to lookup conn for request")
			return nil, &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
		if conn != nil {
			log.Info().Str("conn_id", conn.ConnID.String()).Msg("found existing connection")
			return conn, nil
		}
		// conn doesn't exist, we probably nuked it.
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        fmt.Errorf("session expired"),
		}
	}

	// We're going to make a new connection
	// Ensure we have the v2 side of things hooked up
	v2device, err := h.V2Store.InsertDevice(deviceID)
	if err != nil {
		log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to insert v2 device")
		return nil, &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
	}
	if v2device.UserID == "" {
		v2device.UserID, err = h.V2.WhoAmI(req.Header.Get("Authorization"))
		if err != nil {
			log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to get user ID from device ID")
			return nil, &internal.HandlerError{
				StatusCode: http.StatusBadGateway,
				Err:        err,
			}
		}
		if err = h.V2Store.UpdateUserIDForDevice(deviceID, v2device.UserID); err != nil {
			log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to persist user ID -> device ID mapping")
			// non-fatal, we can still work without doing this
		}
	}
	h.PollerMap.EnsurePolling(
		req.Header.Get("Authorization"), v2device.UserID, v2device.DeviceID, v2device.Since,
		hlog.FromRequest(req).With().Str("user_id", v2device.UserID).Logger(),
	)

	// Now the v2 side of things are running, we can make a v3 live sync conn
	// NB: this isn't inherently racey (we did the check for an existing conn before EnsurePolling)
	// because we *either* do the existing check *or* make a new conn. It's important for CreateConn
	// to check for an existing connection though, as it's possible for the client to call /sync
	// twice for a new connection and get the same session ID.
	conn = h.Notifier.GetOrCreateConn(ConnID{
		SessionID: h.generateSessionID(),
		DeviceID:  deviceID,
	}, v2device.UserID)
	log.Info().Str("conn_id", conn.ConnID.String()).Msg("creating new connection")
	return conn, nil
}

func (h *SyncLiveHandler) generateSessionID() string {
	return "1"
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) UpdateDeviceSince(deviceID, since string) error {
	return h.V2Store.UpdateDeviceSince(deviceID, since)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Accumulate(roomID string, timeline []json.RawMessage) error {
	numNew, err := h.Storage.Accumulate(roomID, timeline)
	if err != nil {
		return err
	}
	if numNew == 0 {
		// no new events
		return nil
	}
	newEvents := timeline[len(timeline)-numNew:]

	// we have new events, let the notifier handle them
	for _, event := range newEvents {
		var stateKey *string
		ev := gjson.ParseBytes(event)
		if sk := ev.Get("state_key"); sk.Exists() {
			stateKey = &sk.Str
		}
		h.Notifier.OnNewEvent(
			roomID, ev.Get("sender").Str, ev.Get("type").Str, stateKey, ev.Get("content"),
		)
	}
	return err
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Initialise(roomID string, state []json.RawMessage) error {
	added, err := h.Storage.Initialise(roomID, state)
	if err != nil {
		return err
	}
	if !added {
		// no new events
		return nil
	}
	// we have new events, let the notifier handle them
	for _, event := range state {
		var stateKey *string
		ev := gjson.ParseBytes(event)
		if sk := ev.Get("state_key"); sk.Exists() {
			stateKey = &sk.Str
		}
		h.Notifier.OnNewEvent(
			roomID, ev.Get("sender").Str, ev.Get("type").Str, stateKey, ev.Get("content"),
		)
	}
	return err
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) SetTyping(roomID string, userIDs []string) (int64, error) {
	return h.Storage.TypingTable.SetTyping(roomID, userIDs)
}

// Called from the v2 poller, implements V2DataReceiver
// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *SyncLiveHandler) AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) error {
	_, err := h.Storage.ToDeviceTable.InsertMessages(deviceID, msgs)
	return err
}