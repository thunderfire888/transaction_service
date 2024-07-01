package logic

import (
	"context"
	"github.com/thunderfire888/transaction_service/common/response"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/commissionService"
	"github.com/thunderfire888/transaction_service/rpc/internal/types"
	"github.com/thunderfire888/transaction_service/rpc/transactionclient"
	"gorm.io/gorm"
	"strconv"
	"strings"

	"github.com/thunderfire888/transaction_service/rpc/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type RecalculateCommissionMonthReportLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewRecalculateCommissionMonthReportLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RecalculateCommissionMonthReportLogic {
	return &RecalculateCommissionMonthReportLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *RecalculateCommissionMonthReportLogic) RecalculateCommissionMonthReport(in *transactionclient.RecalculateCommissionMonthReportRequest) (*transactionclient.RecalculateCommissionMonthReportResponse, error) {
	// todo: add your logic here and delete this line
	var report types.CommissionMonthReportX

	// 取得報表
	if err := l.svcCtx.MyDB.Table("cm_commission_month_reports").Where("id = ?", in.ID).Find(&report).Error; err != nil {
		return &transactionclient.RecalculateCommissionMonthReportResponse{
			Code:    response.DATABASE_FAILURE,
			Message: err.Error(),
		}, nil
	}

	if report.Status == "1" {
		// 已審核報表不可再重新計算
		return &transactionclient.RecalculateCommissionMonthReportResponse{
			Code:    response.MERCHANT_COMMISSION_AUDIT,
			Message: "報表狀態錯誤",
		}, nil
	}

	monthArray := strings.Split(report.Month, "-")
	y, err1 := strconv.Atoi(monthArray[0])
	m, err2 := strconv.Atoi(monthArray[1])
	if err1 != nil || err2 != nil {
		// todo: 時間格是錯誤
	}

	// 取得此月份起訖時間
	startAt := commissionService.BeginningOfMonth(y, m).Format("2006-01-02 15:04:05")
	endAt := commissionService.EndOfMonth(y, m).Format("2006-01-02 15:04:05")

	if err := l.svcCtx.MyDB.Transaction(func(txdb *gorm.DB) (err error) {
		// 刪除舊的報表鄉情
		if err3 := txdb.Table("cm_commission_month_report_details").Where("commission_month_report_id = ?", in.ID).
			Delete(types.CommissionMonthReportDetail{}).Error; err3 != nil {
				return err3
		}
		// 重新計算
		return commissionService.CalculateMonthReport(txdb, report, startAt, endAt)
	}); err != nil {
		return &transactionclient.RecalculateCommissionMonthReportResponse{
			Code:    response.DATABASE_FAILURE,
			Message: err.Error(),
		}, nil
	}

	return &transactionclient.RecalculateCommissionMonthReportResponse{
		Code:    response.API_SUCCESS,
		Message: "操作成功",
	}, nil
}
