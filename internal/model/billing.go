package model

import (
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"
)

// CheckBalance 检查指定 AppKey 的余额是否充足
// 返回值: bool 表示是否允许放行, error 表示数据库查询错误
func CheckBalance(appKeyID uint) (bool, error) {
	var appkey AppKey
	if err := DB.Select("balance").First(&appkey, appKeyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("no find this appkey")
			return false, nil
		}
		return false, err
	}
	if appkey.Balance <= 0 {
		return false, nil
	}
	return true, nil
}

func DeductBalance(appKeyID uint, modelName string, promptTokens int, completionTokens int) error {
	// 题目要求 1. 根据 modelName 查单价
	var pricing Pricing
	err := DB.Where("model_name = ?", modelName).First(&pricing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 找不到定价，按 0 元处理，不阻断正常业务
			log.Printf("⚠️ 警告：找不到模型 [%s] 的定价配置，本次调用免费", modelName)
			return nil
		}
		// 数据库查询出错
		return fmt.Errorf("查询模型定价失败: %w", err)
	}

	// 题目要求 2. 计算本次调用的总花费
	// 价格通常是按 1K tokens 计费。
	// 这里要注意将 int 转换为 float64 进行计算，否则整数除法会丢失精度。
	promptCost := float64(promptTokens) / 1000.0 * pricing.PromptPrice
	completionCost := float64(completionTokens) / 1000.0 * pricing.CompletionPrice
	totalCostFloat := promptCost + completionCost

	// 转换为“微额度”（乘以 10000），再转为 int64 以适配数据库字段
	// （大厂标准：金额存储一律禁止使用 float/double，必须用放大倍数后的 int/bigint，防止精度丢失）
	cost := int64(totalCostFloat * 10000)

	// 如果根本没有产生费用，直接返回
	if cost == 0 {
		return nil
	}

	// 题目要求 3 & 4. 扣除余额 (数据库级原子操作)
	// 使用 gorm.Expr 进行原子扣减：UPDATE app_keys SET balance = balance - cost WHERE id = appKeyID
	result := DB.Model(&AppKey{}).
		Where("id = ?", appKeyID).
		UpdateColumn("balance", gorm.Expr("balance - ?", cost))

	if result.Error != nil {
		return fmt.Errorf("扣除 AppKey[%d] 余额失败: %w", appKeyID, result.Error)
	}

	// 严谨起见，判断是否有记录被更新
	if result.RowsAffected == 0 {
		return fmt.Errorf("AppKey[%d] 不存在或已被删除", appKeyID)
	}

	return nil
}
