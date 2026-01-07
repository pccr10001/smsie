package repository

import (
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

type SMSRepository struct {
	db *gorm.DB
}

func NewSMSRepository(db *gorm.DB) *SMSRepository {
	return &SMSRepository{db: db}
}

func (r *SMSRepository) Create(sms *model.SMS) error {
	return r.db.Create(sms).Error
}

func (r *SMSRepository) FindByICCID(iccid string) ([]model.SMS, error) {
	var smsList []model.SMS
	err := r.db.Where("iccid = ?", iccid).Order("timestamp desc").Find(&smsList).Error
	return smsList, err
}
