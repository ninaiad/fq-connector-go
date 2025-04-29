package mongodb

import (
	"encoding/hex"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"

	"github.com/ydb-platform/ydb-go-genproto/protos/Ydb"

	api_service_protos "github.com/ydb-platform/fq-connector-go/api/service/protos"
	"github.com/ydb-platform/fq-connector-go/common"
)

func makeFilter(
	logger *zap.Logger,
	split *api_service_protos.TSplit,
	filtering api_service_protos.TReadSplitsRequest_EFiltering,
) (bson.D, *options.FindOptions, error) {
	opts := options.Find()

	what := split.Select.What
	if what == nil {
		return nil, nil, fmt.Errorf("not specified columns to query in Select.What")
	}

	projection := bson.D{}
	for _, item := range what.GetItems() {
		projection = append(projection, bson.E{Key: item.GetColumn().Name, Value: 1})
	}

	opts.SetProjection(projection)

	limit := split.Select.Limit
	if limit != nil {
		opts.SetSkip(int64(limit.Offset))
		opts.SetLimit(int64(limit.Limit))
	}

	where := split.Select.Where

	filterTyped := where.GetFilterTyped()
	if filterTyped == nil {
		return bson.D{}, opts, nil
	}

	doSuppressConjunctionErrors := filtering == api_service_protos.TReadSplitsRequest_FILTERING_OPTIONAL

	filter, err := makePredicateFilter(logger, filterTyped, doSuppressConjunctionErrors)
	if err != nil {
		switch filtering {
		case api_service_protos.TReadSplitsRequest_FILTERING_MANDATORY:
			return nil, nil, fmt.Errorf("encountered an error making a filter: %w", err)
		case api_service_protos.TReadSplitsRequest_FILTERING_UNSPECIFIED,
			api_service_protos.TReadSplitsRequest_FILTERING_OPTIONAL:
			if common.OptionalFilteringAllowedErrors.Match(err) {
				logger.Warn("considering pushdown error as acceptable", zap.Error(err))
				return filter, opts, nil
			}

			return nil, nil, fmt.Errorf("encountered an error making a filter: %w", err)
		default:
			return nil, nil, fmt.Errorf("unknown filtering mode: %d", filtering)
		}
	}

	return filter, opts, nil
}

//nolint:funlen,gocyclo
func makePredicateFilter(
	logger *zap.Logger,
	predicate *api_service_protos.TPredicate,
	maySuppressConjunctionErrors bool,
) (bson.D, error) {
	var (
		result bson.D
		err    error
	)

	switch p := predicate.Payload.(type) {
	case *api_service_protos.TPredicate_IsNull:
		result, err = getIsNullFilter(p.IsNull.GetValue())
		if err != nil {
			return result, fmt.Errorf("get IsNull filter: %w", err)
		}
	case *api_service_protos.TPredicate_IsNotNull:
		result, err = getIsNotNullFilter(p.IsNotNull.GetValue())
		if err != nil {
			return result, fmt.Errorf("get IsNotNull filter: %w", err)
		}
	case *api_service_protos.TPredicate_Negation:
		result, err = getNegationFilter(logger, p.Negation)
		if err != nil {
			return result, fmt.Errorf("get Negation filter: %w", err)
		}
	case *api_service_protos.TPredicate_Conjunction:
		result, err = getConjunctionFilter(logger, p.Conjunction, maySuppressConjunctionErrors)
		if err != nil {
			return result, fmt.Errorf("get Conjunction filter: %w", err)
		}
	case *api_service_protos.TPredicate_Disjunction:
		result, err = getDisjunctionFilter(logger, p.Disjunction)
		if err != nil {
			return result, fmt.Errorf("get Disjunction filter: %w", err)
		}
	case *api_service_protos.TPredicate_Comparison:
		result, err = getComparisonFilter(p.Comparison)
		if err != nil {
			return result, fmt.Errorf("get Comparison filter: %w", err)
		}
	case *api_service_protos.TPredicate_BoolExpression:
		result, err = getBooleanFilter(p.BoolExpression)
		if err != nil {
			return result, fmt.Errorf("get BoolExpression filter: %w", err)
		}
	case *api_service_protos.TPredicate_In:
		result, err = getInSetFilter(p.In)
		if err != nil {
			return result, fmt.Errorf("get InSet filter: %w", err)
		}
	case *api_service_protos.TPredicate_Between:
		result, err = getBetweenFilter(p.Between)
		if err != nil {
			return result, fmt.Errorf("get Between filter: %w", err)
		}
	case *api_service_protos.TPredicate_Regexp:
		result, err = getRegexFilter(p.Regexp)
		if err != nil {
			return result, fmt.Errorf("get Regexp filter: %w", err)
		}
	default:
		return nil, fmt.Errorf("%w, type: %T", common.ErrUnimplementedPredicateType, p)
	}

	return result, nil
}

func getNegationFilter(
	logger *zap.Logger,
	negation *api_service_protos.TPredicate_TNegation,
) (bson.D, error) {
	operand, err := makePredicateFilter(logger, negation.Operand, false)
	if err != nil {
		return nil, fmt.Errorf("unable to format negation operand predicate: %w", err)
	}

	return bson.D{{Key: "$nor", Value: bson.A{operand}}}, nil
}

func getBooleanFilter(
	boolExpression *api_service_protos.TPredicate_TBoolExpression,
) (bson.D, error) {
	expr, err := formatExpression(boolExpression.Value)
	if err != nil {
		return nil, fmt.Errorf("unable to format bool expression: %w", err)
	}

	return bson.D{{Key: "$expr", Value: bson.D{{Key: "$eq", Value: bson.A{expr, true}}}}}, nil
}

func getConjunctionFilter(
	logger *zap.Logger,
	conjunction *api_service_protos.TPredicate_TConjunction,
	suppressErrors bool,
) (bson.D, error) {
	operands := make([]bson.D, 0, len(conjunction.Operands))

	for _, op := range conjunction.Operands {
		operand, err := makePredicateFilter(logger, op, false)
		if err != nil {
			err = fmt.Errorf("unable to format one of the predicates in conjunction: %w", err)
			if !suppressErrors {
				return nil, err
			}

			logger.Warn(err.Error())
		}

		operands = append(operands, operand)
	}

	return bson.D{{Key: "$and", Value: operands}}, nil
}

func getDisjunctionFilter(
	logger *zap.Logger,
	disjunction *api_service_protos.TPredicate_TDisjunction,
) (bson.D, error) {
	operands := make([]bson.D, 0, len(disjunction.Operands))

	for _, op := range disjunction.Operands {
		operand, err := makePredicateFilter(logger, op, false)
		if err != nil {
			return nil, fmt.Errorf("unable to format one of the predicates in disjunction: %w", err)
		}

		operands = append(operands, operand)
	}

	return bson.D{{Key: "$or", Value: operands}}, nil
}

func getIsNotNullFilter(
	expression *api_service_protos.TExpression,
) (bson.D, error) {
	switch e := expression.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		return bson.D{{Key: e.Column, Value: bson.D{{Key: "$ne", Value: nil}}}}, nil
	default:
		return nil, fmt.Errorf("unsupported expression in IsNotNull filter: %w", common.ErrUnimplementedExpression)
	}
}

func getIsNullFilter(
	expression *api_service_protos.TExpression,
) (bson.D, error) {
	switch e := expression.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		return bson.D{
			{Key: "$or", Value: bson.A{
				bson.D{{Key: e.Column, Value: bson.D{{Key: "$exists", Value: false}}}},
				bson.D{{Key: e.Column, Value: bson.D{{Key: "$eq", Value: nil}}}},
			}},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported expression in IsNull filter: %w", common.ErrUnimplementedExpression)
	}
}

func getComparisonFilter(
	comparison *api_service_protos.TPredicate_TComparison,
) (bson.D, error) {
	var operation string

	switch op := comparison.Operation; op {
	case api_service_protos.TPredicate_TComparison_L:
		operation = "$lt"
	case api_service_protos.TPredicate_TComparison_LE:
		operation = "$lte"
	case api_service_protos.TPredicate_TComparison_EQ:
		operation = "$eq"
	case api_service_protos.TPredicate_TComparison_NE:
		operation = "$ne"
	case api_service_protos.TPredicate_TComparison_GE:
		operation = "$gte"
	case api_service_protos.TPredicate_TComparison_G:
		operation = "$gt"
	case api_service_protos.TPredicate_TComparison_STARTS_WITH,
		api_service_protos.TPredicate_TComparison_ENDS_WITH,
		api_service_protos.TPredicate_TComparison_CONTAINS:
		return getStringComparisonFilter(comparison)
	default:
		return nil, fmt.Errorf("%w, op: %d", common.ErrUnimplementedOperation, op)
	}

	left, err := formatExpression(comparison.LeftValue)
	if err != nil {
		return nil, fmt.Errorf("format left expression: %v: %w", comparison.LeftValue, err)
	}

	right, err := formatExpression(comparison.RightValue)
	if err != nil {
		return nil, fmt.Errorf("format right expression: %v: %w", comparison.RightValue, err)
	}

	return bson.D{{Key: "$expr", Value: bson.D{{Key: operation, Value: bson.A{left, right}}}}}, nil
}

func getStringComparisonFilter(
	comparison *api_service_protos.TPredicate_TComparison,
) (bson.D, error) {
	var (
		pattern   string
		fieldName string
	)

	switch e := comparison.LeftValue.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		fieldName = e.Column
	default:
		return nil, fmt.Errorf("unsupported expression for left value in string comparison filter: %w", common.ErrUnimplementedExpression)
	}

	switch value := comparison.RightValue.Payload.(type) {
	case *api_service_protos.TExpression_TypedValue:
		pattern = value.TypedValue.Value.GetTextValue()
		if pattern == "" {
			return nil, fmt.Errorf("failed to get string from right value %v in string comparison filter %v", value, comparison.Operation)
		}
	default:
		return nil, fmt.Errorf("unsupported right value %v in string comparison filter %v", value, comparison.Operation)
	}

	switch op := comparison.Operation; op {
	case api_service_protos.TPredicate_TComparison_STARTS_WITH:
		pattern = fmt.Sprintf("^%s", pattern)
	case api_service_protos.TPredicate_TComparison_ENDS_WITH:
		pattern = fmt.Sprintf("%s$", pattern)
	case api_service_protos.TPredicate_TComparison_CONTAINS:
	default:
		return nil, fmt.Errorf("%w in string comparison: %d", common.ErrUnimplementedOperation, op)
	}

	return bson.D{{Key: fieldName, Value: bson.D{{Key: "$regex", Value: pattern}}}}, nil
}

func getInSetFilter(
	in *api_service_protos.TPredicate_TIn,
) (bson.D, error) {
	var fieldName string
	switch e := in.Value.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		fieldName = e.Column
	default:
		return nil, fmt.Errorf("unsupported expression in In filter: %w", common.ErrUnimplementedExpression)
	}

	inSet := make([]any, 0, len(in.Set))

	for _, e := range in.Set {
		expr, err := formatExpression(e)
		if err != nil {
			return nil, fmt.Errorf("unsupported expression in In filter's Set: %w", common.ErrUnimplementedExpression)
		}

		inSet = append(inSet, expr)
	}

	return bson.D{{Key: fieldName, Value: bson.D{{Key: "$in", Value: inSet}}}}, nil
}

func getBetweenFilter(
	between *api_service_protos.TPredicate_TBetween,
) (bson.D, error) {
	var fieldName string

	switch e := between.Value.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		fieldName = e.Column
	default:
		return nil, fmt.Errorf("unsupported expression in Between filter: %w", common.ErrUnimplementedExpression)
	}

	least, err := formatExpression(between.Least)
	if err != nil {
		return nil, fmt.Errorf("format least expression: %v: %w", between.Least, err)
	}

	greatest, err := formatExpression(between.Greatest)
	if err != nil {
		return nil, fmt.Errorf("format greatest expression: %v: %w", between.Greatest, err)
	}

	return bson.D{{
		Key: fieldName,
		Value: bson.D{
			{Key: "$gte", Value: least},
			{Key: "$lte", Value: greatest},
		},
	}}, nil
}

func getRegexFilter(
	regex *api_service_protos.TPredicate_TRegexp,
) (bson.D, error) {
	var fieldName string

	switch e := regex.Value.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		fieldName = e.Column
	default:
		return nil, fmt.Errorf("unsupported expression in Regexp filter: %w", common.ErrUnimplementedExpression)
	}

	pattern, err := formatExpression(regex.Pattern)
	if err != nil {
		return nil, fmt.Errorf("format regex pattern expression: %v: %w", regex.Pattern, err)
	}

	return bson.D{{Key: fieldName, Value: bson.D{{Key: "$regex", Value: pattern}}}}, nil
}

func formatExpression(expression *api_service_protos.TExpression) (any, error) {
	switch e := expression.Payload.(type) {
	case *api_service_protos.TExpression_Column:
		return fmt.Sprintf("$%s", e.Column), nil
	case *api_service_protos.TExpression_TypedValue:
		return formatTypedValue(e.TypedValue)
	case *api_service_protos.TExpression_Null:
		return nil, nil
	case *api_service_protos.TExpression_Coalesce:
		return formatCoalesce(e.Coalesce)
	default:
		return nil, fmt.Errorf("%w, type: %T", common.ErrUnimplementedExpression, e)
	}
}

func formatTypedValue(expr *Ydb.TypedValue) (any, error) {
	v := expr.GetValue()
	ydbType := expr.GetType()

	if v == nil || v.Value == nil || ydbType == nil {
		return nil, fmt.Errorf("got %v of type %T as null trying to format Typed Value expression", v, v)
	}

	var value any
	switch t := v.Value.(type) {
	case *Ydb.Value_BoolValue:
		value = t.BoolValue
	case *Ydb.Value_Int32Value:
		value = t.Int32Value
	case *Ydb.Value_Uint32Value:
		value = t.Uint32Value
	case *Ydb.Value_Int64Value:
		value = t.Int64Value
	case *Ydb.Value_Uint64Value:
		value = t.Uint64Value
	case *Ydb.Value_FloatValue:
		value = t.FloatValue
	case *Ydb.Value_DoubleValue:
		value = t.DoubleValue
	case *Ydb.Value_BytesValue:
		value = t.BytesValue
	case *Ydb.Value_TextValue:
		value = t.TextValue
	default:
		return nil, fmt.Errorf("%w, type: %T", common.ErrUnimplementedTypedValue, t)
	}

	value, err := formatValue(ydbType, value)
	if err != nil {
		return nil, fmt.Errorf("%w %w", err, common.ErrUnimplementedTypedValue)
	}

	return value, nil
}

func formatCoalesce(expr *api_service_protos.TExpression_TCoalesce) (any, error) {
	operands := make([]any, 0, len(expr.Operands))

	for _, opExpr := range expr.Operands {
		op, err := formatExpression(opExpr)
		if err != nil {
			return nil, fmt.Errorf("format coalesce expression: %w", err)
		}

		operands = append(operands, op)
	}

	return bson.D{{Key: "$ifNull", Value: operands}}, nil
}

func formatValue(exprType *Ydb.Type, value any) (any, error) {
	for exprType.GetOptionalType() != nil {
		exprType = exprType.GetOptionalType().GetItem()
	}

	switch t := exprType.Type.(type) {
	case *Ydb.Type_TypeId:
		switch t.TypeId {
		case Ydb.Type_BOOL, Ydb.Type_INT8, Ydb.Type_UINT8, Ydb.Type_INT16,
			Ydb.Type_UINT16, Ydb.Type_INT32, Ydb.Type_UINT32, Ydb.Type_INT64,
			Ydb.Type_UINT64, Ydb.Type_FLOAT, Ydb.Type_DOUBLE, Ydb.Type_STRING, Ydb.Type_UTF8:
			return value, nil
		default:
			return nil, fmt.Errorf("unsupported type %T for typed value", t)
		}
	case *Ydb.Type_TaggedType:
		if !common.TypesEqual(exprType, objectIdType) {
			return nil, fmt.Errorf("unknown Tagged type: %T", exprType)
		}

		return tryFormatObjectId(value)
	default:
		return nil, fmt.Errorf("unsupported type %T for typed value", t)
	}
}

func tryFormatObjectId(value any) (any, error) {
	switch v := value.(type) {
	case []byte:
		hexString := hex.EncodeToString(v)

		objectId, err := primitive.ObjectIDFromHex(hexString)
		if err != nil {
			return nil, fmt.Errorf("failed to construct ObjectId from %s: %w", hexString, err)
		}

		return objectId, nil
	default:
		return nil, fmt.Errorf("wrong value of TypedValue for ObjectId: %v", value)
	}
}
