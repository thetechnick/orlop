package handlers

import (
	"github.com/thetechnick/orlop/pkg/apiserver/constants"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/thetechnick/orlop/pkg/apiserver/storage"
	"k8s.io/apimachinery/pkg/api/meta"
)

// watchParams holds parsed watch request parameters
type watchParams struct {
	resourceVersion      string
	allowWatchBookmarks  bool
	sendInitialEvents    bool
	resourceVersionMatch string
	timeoutSeconds       string
}

// watchContext holds state for a watch operation
type watchContext struct {
	handler                *ResourceHandler
	writer                 http.ResponseWriter
	flusher                http.Flusher
	encoder                *json.Encoder
	lastResourceVersion    string
	currentResourceVersion string
	initialBookmarkSent    bool
	listIsEmpty            bool
	shardSelector          *storage.ShardSelector
}

// handleWatch implements the Kubernetes watch protocol
func (h *ResourceHandler) handleWatch(w http.ResponseWriter, r *http.Request, opts storage.ListOptions, shardSelector *storage.ShardSelector) {
	params := h.parseWatchParams(r)
	
	h.logger.V(1).Info("Watch parameters",
		params.allowWatchBookmarks, params.sendInitialEvents, params.resourceVersionMatch, params.timeoutSeconds)

	// Apply timeout if specified
	ctx := h.applyWatchTimeout(r.Context(), params.timeoutSeconds)

	// Start watch
	eventCh, stop, err := h.store.Watch(opts, params.resourceVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to start watch: %v", err))
		return
	}
	defer stop()

	// Set up streaming response
	flusher := h.setupWatchResponse(w)
	if flusher == nil {
		return
	}

	// Initialize watch context
	wctx := h.initWatchContext(w, flusher, opts, params.resourceVersion, shardSelector)

	// Send initial events if requested
	if params.sendInitialEvents {
		h.sendInitialResourceEvents(wctx, opts, params.allowWatchBookmarks)
	}

	// Send initial bookmark if appropriate
	if !params.sendInitialEvents && params.allowWatchBookmarks {
		h.sendInitialBookmark(wctx, params.resourceVersion)
	}

	// Set up periodic bookmarks
	var bookmarkCh <-chan time.Time
	if params.allowWatchBookmarks {
		bookmarkCh = h.setupPeriodicBookmarks()
	}

	// Stream watch events
	h.streamResourceEvents(ctx, wctx, eventCh, bookmarkCh, params.allowWatchBookmarks)
}

// parseWatchParams extracts and parses watch parameters from the request
func (h *ResourceHandler) parseWatchParams(r *http.Request) watchParams {
	query := r.URL.Query()
	return watchParams{
		resourceVersion:      query.Get("resourceVersion"),
		allowWatchBookmarks:  query.Get("allowWatchBookmarks") == "true",
		sendInitialEvents:    query.Get("sendInitialEvents") == "true",
		resourceVersionMatch: query.Get("resourceVersionMatch"),
		timeoutSeconds:       query.Get("timeoutSeconds"),
	}
}

// applyWatchTimeout applies the specified timeout to the context
func (h *ResourceHandler) applyWatchTimeout(ctx context.Context, timeoutSeconds string) context.Context {
	if timeoutSeconds == "" {
		return ctx
	}

	timeout, err := strconv.ParseInt(timeoutSeconds, 10, 64)
	if err != nil || timeout <= 0 {
		return ctx
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	_ = cancel // Will be called when parent context is done
	return ctxWithTimeout
}

// setupWatchResponse configures HTTP headers for streaming and returns the flusher
func (h *ResourceHandler) setupWatchResponse(w http.ResponseWriter) http.Flusher {
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.Header().Set(constants.HeaderTransferEncoding, constants.TransferEncodingChunked)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.WriteHeader(http.StatusOK)
	// CRITICAL: Flush immediately to send headers before streaming events
	flusher.Flush()

	return flusher
}

// initWatchContext initializes the watch context with current state
func (h *ResourceHandler) initWatchContext(w http.ResponseWriter, flusher http.Flusher, opts storage.ListOptions, resourceVersion string, shardSelector *storage.ShardSelector) *watchContext {
	// Get current resource version and check if list is empty
	currentRV, isEmpty := h.getCurrentResourceVersion(opts)

	requestedRV := resourceVersion
	if requestedRV == "" {
		requestedRV = "0"
	}

	return &watchContext{
		handler:                h,
		writer:                 w,
		flusher:                flusher,
		encoder:                json.NewEncoder(w),
		lastResourceVersion:    currentRV,
		currentResourceVersion: currentRV,
		initialBookmarkSent:    false,
		listIsEmpty:            isEmpty,
		shardSelector:          shardSelector,
	}
}

// getCurrentResourceVersion retrieves the current resource version from the store
func (h *ResourceHandler) getCurrentResourceVersion(opts storage.ListOptions) (string, bool) {
	list, err := h.store.List(opts)
	if err != nil {
		return "0", true
	}

	items, _ := meta.ExtractList(list)
	return list.GetResourceVersion(), len(items) == 0
}

// sendInitialResourceEvents sends existing objects as ADDED events when sendInitialEvents=true
func (h *ResourceHandler) sendInitialResourceEvents(wctx *watchContext, opts storage.ListOptions, allowBookmarks bool) {
	list, err := wctx.handler.store.List(opts)
	if err != nil {
		return
	}

	items, _ := meta.ExtractList(list)

	// Count items and filter by shard
	sentCount := 0
	for _, item := range items {
		// Note: Shard filtering is now handled by storage.List() via opts.ShardSelector
		// No need to filter here

		watchEvent := map[string]interface{}{
			"type":   string(storage.EventAdded),
			"object": item,
		}
		if err := wctx.encoder.Encode(watchEvent); err != nil {
			return
		}
		wctx.flusher.Flush()
		sentCount++
	}

	h.logger.V(1).Info("Sent initial events", "total", len(items), "sent", sentCount, "shard", wctx.shardSelector)

	// Send bookmark marking end of initial events
	if allowBookmarks {
		wctx.sendBookmark(map[string]interface{}{
			constants.AnnotationInitialEventsEnd: "true",
		})
		wctx.initialBookmarkSent = true
	}
}

// sendInitialBookmark sends an initial bookmark if already caught up or list is empty
func (h *ResourceHandler) sendInitialBookmark(wctx *watchContext, requestedRV string) {
	if requestedRV == "" {
		requestedRV = "0"
	}

	requestedRVInt, _ := strconv.ParseInt(requestedRV, 10, 64)
	currentRVInt, _ := strconv.ParseInt(wctx.currentResourceVersion, 10, 64)

	// Send bookmark if caught up or no historical events to send
	if requestedRVInt >= currentRVInt || wctx.listIsEmpty {
		wctx.sendBookmark(nil)
		wctx.initialBookmarkSent = true
	}
}

// setupPeriodicBookmarks creates a ticker for periodic bookmark events
func (h *ResourceHandler) setupPeriodicBookmarks() <-chan time.Time {
	ticker := time.NewTicker(30 * time.Second)
	return ticker.C
}

// streamResourceEvents handles the main event streaming loop
func (h *ResourceHandler) streamResourceEvents(ctx context.Context, wctx *watchContext, eventCh <-chan storage.ResourceEvent, bookmarkCh <-chan time.Time, allowBookmarks bool) {
	currentRVInt, _ := strconv.ParseInt(wctx.currentResourceVersion, 10, 64)

	for {
		select {
		case <-ctx.Done():
			return

		case <-bookmarkCh:
			// Send periodic bookmark
			wctx.sendBookmark(nil)

		case event, ok := <-eventCh:
			if !ok {
				return
			}

			// Update last resource version
			wctx.lastResourceVersion = event.ResourceVersion

			// Note: Shard filtering is now handled by storage.Watch() via opts.ShardSelector
			// No need to filter here

			// Send watch event
			watchEvent := map[string]interface{}{
				"type":   string(event.Type),
				"object": event.Object,
			}

			if err := wctx.encoder.Encode(watchEvent); err != nil {
				return
			}
			wctx.flusher.Flush()

			// Send initial bookmark after catching up
			if allowBookmarks && !wctx.initialBookmarkSent {
				eventRVInt, _ := strconv.ParseInt(event.ResourceVersion, 10, 64)
				if eventRVInt >= currentRVInt {
					wctx.sendBookmark(nil)
					wctx.initialBookmarkSent = true
				}
			}
		}
	}
}

// sendBookmark sends a BOOKMARK event with optional annotations
func (wctx *watchContext) sendBookmark(annotations map[string]interface{}) {
	metadata := map[string]interface{}{
		"resourceVersion": wctx.lastResourceVersion,
	}
	if annotations != nil {
		metadata["annotations"] = annotations
	}

	bookmarkObj := map[string]interface{}{
		"apiVersion": wctx.handler.gvk.GroupVersion().String(),
		"kind":       wctx.handler.gvk.Kind,
		constants.FieldMetadata:   metadata,
	}

	watchEvent := map[string]interface{}{
		"type":   string(storage.EventBookmark),
		"object": bookmarkObj,
	}

	wctx.encoder.Encode(watchEvent)
	wctx.flusher.Flush()
}
