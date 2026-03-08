package repository

import (
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ModemRepository struct {
	db *gorm.DB
}

func NewModemRepository(db *gorm.DB) *ModemRepository {
	return &ModemRepository{db: db}
}

func (r *ModemRepository) Upsert(modem *model.Modem) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "iccid"}},
		DoUpdates: clause.AssignmentColumns([]string{"imei", "port_name"}),
	}).Create(modem).Error
}

func (r *ModemRepository) FindByICCID(iccid string) (*model.Modem, error) {
	var modem model.Modem
	err := r.db.First(&modem, "iccid = ?", iccid).Error
	return &modem, err
}

func (r *ModemRepository) MarkAllOffline() {
	// Runtime status is in-memory and should not be persisted.
}
