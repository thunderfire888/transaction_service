package logic

import (
	"context"
	"fmt"
	"github.com/thunderfire888/transaction_service/common/constants"
	"github.com/thunderfire888/transaction_service/common/response"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/merchantbalanceservice"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/orderfeeprofitservice"
	"github.com/thunderfire888/transaction_service/rpc/internal/svc"
	"github.com/thunderfire888/transaction_service/rpc/internal/types"
	"github.com/thunderfire888/transaction_service/rpc/transactionclient"
	"github.com/gioco-play/easy-i18n/i18n"
	"github.com/neccoys/go-zero-extension/redislock"
	"gorm.io/gorm"

	"github.com/zeromicro/go-zero/core/logx"
)

type ConfirmPayOrderTransactionLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewConfirmPayOrderTransactionLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ConfirmPayOrderTransactionLogic {
	return &ConfirmPayOrderTransactionLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *ConfirmPayOrderTransactionLogic) ConfirmPayOrderTransaction(in *transactionclient.ConfirmPayOrderRequest) (*transactionclient.ConfirmPayOrderResponse, error) {
	var order *types.OrderX

	myDB := l.svcCtx.MyDB

	if err := myDB.Table("tx_orders").
		Where("order_no = ?", in.OrderNo).Take(&order).Error; err != nil {
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.ORDER_NUMBER_NOT_EXIST,
			Message: "商户订单号不存在 ",
		}, nil
	}

	// 收款單才能確認收款
	if order.Type != constants.ORDER_TYPE_ZF {
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.ORDER_TYPE_IS_WRONG,
			Message: "訂單類型錯誤 ",
		}, nil
	}

	// 處理中的且非鎖定訂單 才能確認收款
	if order.Status != "1" {
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.ORDER_STATUS_WRONG,
			Message: "订单状态非处理中 ",
		}, nil
	}
	if order.IsLock == "1" {
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.ORDER_IS_STATUS_IS_LOCK,
			Message: "订单号已锁定",
		}, nil
	}

	// 这里谨用于确认是否有设置费率
	var merchantChannelRate *types.MerchantChannelRate
	if err := myDB.Table("mc_merchant_channel_rate").
		Where("merchant_code = ? AND channel_pay_types_code = ?", order.MerchantCode, order.ChannelPayTypesCode).
		Take(&merchantChannelRate).Error; err != nil {
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.RATE_NOT_CONFIGURED,
			Message: "未配置商户渠道费率",
		}, nil
	}
	redisKey := fmt.Sprintf("%s-%s", order.MerchantCode, order.CurrencyCode)
	redisLock := redislock.New(l.svcCtx.RedisClient, redisKey, "merchant-balance:")
	redisLock.SetExpire(8)
	if isOK, redisErr := redisLock.TryLockTimeout(8); isOK {
		defer redisLock.Release()
		/****     交易開始      ****/
		txDB := myDB.Begin()

		// 編輯訂單 異動錢包和餘額
		if err := l.updateOrderAndBalance(txDB, in, order); err != nil {
			txDB.Rollback()
			return &transactionclient.ConfirmPayOrderResponse{
				Code:    response.SYSTEM_ERROR,
				Message: "updateOrderAndBalance 失败",
			}, nil
		}

		if err := txDB.Commit().Error; err != nil {
			txDB.Rollback()
			logx.Errorf("支付確認收款Commit失败，商户号: %s, 订单号: %s, err : %s", order.MerchantCode, order.OrderNo, err.Error())
			return &transactionclient.ConfirmPayOrderResponse{
				Code:    response.DATABASE_FAILURE,
				Message: "资料库错误 Commit失败",
			}, nil
		}
		/****     交易結束      ****/
	} else {
		logx.WithContext(l.ctx).Errorf("商户钱包处理中，Err:%s。 %s", redisErr.Error(), redisKey)
		return &transactionclient.ConfirmPayOrderResponse{
			Code:    response.BALANCE_PROCESSING,
			Message: i18n.Sprintf(response.BALANCE_PROCESSING),
		}, nil
	}

	// 計算利潤 (不抱錯) TODO: 異步??
	if err4 := orderfeeprofitservice.CalculateOrderProfit(l.svcCtx.MyDB, types.CalculateProfit{
		MerchantCode:        order.MerchantCode,
		OrderNo:             order.OrderNo,
		Type:                order.Type,
		CurrencyCode:        order.CurrencyCode,
		BalanceType:         order.BalanceType,
		ChannelCode:         order.ChannelCode,
		ChannelPayTypesCode: order.ChannelPayTypesCode,
		OrderAmount:         order.ActualAmount,
	}); err4 != nil {
		logx.Error("計算利潤出錯:%s", err4.Error())
	}

	// 新單新增訂單歷程 (不抱錯)
	if err4 := l.svcCtx.MyDB.Table("tx_order_actions").Create(&types.OrderActionX{
		OrderAction: types.OrderAction{
			OrderNo:     order.OrderNo,
			Action:      constants.ACTION_SUCCESS,
			UserAccount: order.MerchantCode,
			Comment:     in.Comment,
		},
	}).Error; err4 != nil {
		logx.Error("紀錄訂單歷程出錯:%s", err4.Error())
	}

	return &transactionclient.ConfirmPayOrderResponse{
		Code:                response.API_SUCCESS,
		Message:             "操作成功",
		MerchantCode:        order.MerchantCode,
		MerchantOrderNo:     order.MerchantOrderNo,
		OrderNo:             order.OrderNo,
		OrderAmount:         order.OrderAmount,
		ActualAmount:        order.ActualAmount,
		TransferHandlingFee: order.TransferHandlingFee,
		NotifyUrl:           order.NotifyUrl,
		OrderTime:           order.CreatedAt.Format("20060102150405000"),
		PayOrderTime:        order.TransAt.Time().Format("20060102150405000"),
		Status:              order.Status,
	}, nil
}

func (l *ConfirmPayOrderTransactionLogic) updateOrderAndBalance(db *gorm.DB, req *transactionclient.ConfirmPayOrderRequest, order *types.OrderX) (err error) {

	var merchantBalanceRecord types.MerchantBalanceRecord
	updateBalance := types.UpdateBalance{
		MerchantCode:    order.MerchantCode,
		CurrencyCode:    order.CurrencyCode,
		OrderNo:         order.OrderNo,
		MerchantOrderNo: order.MerchantOrderNo,
		OrderType:       order.Type,
		ChannelCode:     order.ChannelCode,
		PayTypeCode:     order.PayTypeCode,
		TransactionType: "6",
		BalanceType:     order.BalanceType,
		TransferAmount:  order.TransferAmount,
		Comment:         order.Memo,
		CreatedBy:       order.MerchantCode,
	}
	// 異動錢包
	if merchantBalanceRecord, err = merchantbalanceservice.UpdateBalanceForZF(db, l.ctx, l.svcCtx.RedisClient, updateBalance); err != nil {
		return
	}

	// 手動確認收款 實際金額 = 訂單金額
	order.ActualAmount = order.OrderAmount
	order.BeforeBalance = merchantBalanceRecord.BeforeBalance
	order.Balance = merchantBalanceRecord.AfterBalance
	order.TransAt = types.JsonTime{}.New()
	order.Status = "20"
	order.Memo = req.Comment + " \n" + order.Memo

	// 編輯訂單
	if err = db.Table("tx_orders").Updates(&order).Error; err != nil {
		return
	}

	return
}
