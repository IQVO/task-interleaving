package kafka

import (
	"context"
	"fmt"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/task-interleaving/internal/application/usecases"
)

// LayoutTopic is facility-layout's integration topic.
const LayoutTopic = "warehouse.facility.events"

// The facility-layout types this service acts on, byte-identical to its
// AsyncAPI. Every other type on the topic is ignored.
const (
	TypeZoneRegistered             = "com.warehouse.wms.facility-layout.zone.ZoneRegistered"
	TypeLocationSlotRegistered     = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	TypeLocationSlotDecommissioned = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
	TypeLocationGeometryUpdated    = "com.warehouse.wms.facility-layout.locationslot.LocationGeometryUpdated"
)

// The payloads mirror facility-layout's v1 events (restated here, never
// imported). Their extra fields (areaCode, aisleId, locationType, footprint,
// pickSequence, eventName, occurredAt...) are ignored.

type zoneRegisteredData struct {
	ZoneID   string `json:"zoneId"`
	SiteCode string `json:"siteCode"`
	ZoneCode string `json:"zoneCode"`
}

type locationSlotRegisteredData struct {
	LocationCode string `json:"locationCode"`
	ZoneID       string `json:"zoneId"`
}

type locationSlotDecommissionedData struct {
	LocationCode string `json:"locationCode"`
}

// locationGeometryUpdatedData holds the point as pointers: a contract that
// requires xM, yM and zM must not read an absent coordinate as 0 m.
type locationGeometryUpdatedData struct {
	LocationCode string   `json:"locationCode"`
	X            *float64 `json:"xM"`
	Y            *float64 `json:"yM"`
	Z            *float64 `json:"zM"`
}

// geometryChange turns the payload into the use case input, or errBadPayload
// when a coordinate is absent.
func (d locationGeometryUpdatedData) geometryChange() (usecases.GeometryChange, error) {
	if d.X == nil || d.Y == nil || d.Z == nil {
		return usecases.GeometryChange{}, fmt.Errorf("%w: xM, yM and zM are required", errBadPayload)
	}
	return usecases.GeometryChange{Code: d.LocationCode, X: *d.X, Y: *d.Y, Z: *d.Z}, nil
}

// LayoutUseCases are the four use cases the layout consumer dispatches to.
type LayoutUseCases struct {
	Zone           *usecases.ApplyZone
	Registered     *usecases.ApplyLocationRegistered
	Decommissioned *usecases.ApplyLocationDecommissioned
	Geometry       *usecases.ApplyGeometryUpdated
}

// LayoutHandlers maps the facility topic's consumed types to their use cases.
func LayoutHandlers(uc LayoutUseCases, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeZoneRegistered: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[zoneRegisteredData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Zone.Handle(ctx, e.ID(), usecases.ZoneChange{ZoneID: d.ZoneID, SiteCode: d.SiteCode, ZoneCode: d.ZoneCode})
			})
		},
		TypeLocationSlotRegistered: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[locationSlotRegisteredData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Registered.Handle(ctx, e.ID(), usecases.LocationChange{Code: d.LocationCode, ZoneID: d.ZoneID})
			})
		},
		TypeLocationSlotDecommissioned: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[locationSlotDecommissionedData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Decommissioned.Handle(ctx, e.ID(), d.LocationCode)
			})
		},
		TypeLocationGeometryUpdated: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[locationGeometryUpdatedData](e)
			if err != nil {
				return err
			}
			change, err := d.geometryChange()
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.Outcome, error) {
				return uc.Geometry.Handle(ctx, e.ID(), change)
			})
		},
	}
}

// NewLayoutConsumer reads facility-layout's topic under groupID
// (LAYOUT_CONSUMER_GROUP, never a literal).
func NewLayoutConsumer(brokers []string, groupID string, uc LayoutUseCases, logger *slog.Logger) *Consumer {
	return NewLayoutConsumerForTopic(brokers, LayoutTopic, groupID, uc, logger)
}

// NewLayoutConsumerForTopic is NewLayoutConsumer on an explicit topic.
func NewLayoutConsumerForTopic(brokers []string, topic, groupID string, uc LayoutUseCases, logger *slog.Logger) *Consumer {
	return newConsumer("layout consumer", brokers, topic, groupID, LayoutHandlers(uc, logger), logger)
}
