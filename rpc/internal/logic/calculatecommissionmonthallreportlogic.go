package logic

import (
	"context"
	"github.com/thunderfire888/transaction_service/common/response"
	"github.com/thunderfire888/transaction_service/rpc/internal/service/commissionService"
	"github.com/thunderfire888/transaction_service/rpc/transactionclient"

	"github.com/thunderfire888/transaction_service/rpc/internal/svc"
	"github.com/zeromicro/go-zero/core/logx"
)

type CalculateCommissionMonthAllReportLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
	logx.Logger
}

func NewCalculateCommissionMonthAllReportLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CalculateCommissionMonthAllReportLogic {
	return &CalculateCommissionMonthAllReportLogic{
		ctx:    ctx,
		svcCtx: svcCtx,
		Logger: logx.WithContext(ctx),
	}
}

func (l *CalculateCommissionMonthAllReportLogic) CalculateCommissionMonthAllReport(in *transactionclient.CalculateCommissionMonthAllRequest) (*transactionclient.CalculateCommissionMonthAllResponse, error) {

	err := commissionService.CalculateMonthAllReport(l.svcCtx.MyDB, in.Month, l.ctx)
	if err != nil {
		return &transactionclient.CalculateCommissionMonthAllResponse{
			Code:    response.SYSTEM_ERROR,
			Message: err.Error(),
		}, nil
	}

	return &transactionclient.CalculateCommissionMonthAllResponse{
		Code:    response.API_SUCCESS,
		Message: "操作成功",
	}, nil
}
