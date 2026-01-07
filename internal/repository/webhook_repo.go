package repository

import (
	"github.com/pccr10001/smsie/internal/model"
	"gorm.io/gorm"
)

type WebhookRepository struct {
	db *gorm.DB
}

func NewWebhookRepository(db *gorm.DB) *WebhookRepository {
	return &WebhookRepository{db: db}
}

func (r *WebhookRepository) Create(webhook *model.Webhook) error {
	return r.db.Create(webhook).Error
}

func (r *WebhookRepository) FindByICCID(iccid string) ([]model.Webhook, error) {
	var list []model.Webhook
	err := r.db.Where("iccid = ? AND enabled = ?", iccid, true).Find(&list).Error
	return list, err
}

func (r *WebhookRepository) Delete(id uint) error {
	return r.db.Delete(&model.Webhook{}, id).Error
}
