package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (s *Service) liveMemberPhoneMatchSQL() string {
	return phoneLast10SQL("COALESCE(NULLIF(m.whatsapp,''), m.contact_phone)")
}

func (s *Service) liveMyMembershipIDs(ctx context.Context, last10 string, planID int64) ([]int64, error) {
	match := s.liveMemberPhoneMatchSQL()
	q := fmt.Sprintf(`
SELECT mship.id
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE mship.status = 'active' AND %s = $1`, match)
	args := []any{last10}
	if planID > 0 {
		q += ` AND (mship.plan_id = $2 OR mship.id = $2)`
		args = append(args, planID)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id > 0 {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *Service) liveViewerIsPrimaryOnPlan(ctx context.Context, last10 string, planID int64) (bool, error) {
	if planID <= 0 {
		return false, nil
	}
	match := s.liveMemberPhoneMatchSQL()
	q := fmt.Sprintf(`
SELECT mship.primary_member
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE mship.status = 'active' AND (mship.plan_id = $1 OR mship.id = $1) AND %s = $2`, match)
	rows, err := s.DB.QueryContext(ctx, q, planID, last10)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var primary bool
		if err := rows.Scan(&primary); err != nil {
			return false, err
		}
		if primary {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Service) liveMyPlans(ctx context.Context, last10 string, facilityID int64) ([]map[string]any, error) {
	match := s.liveMemberPhoneMatchSQL()
	q := fmt.Sprintf(`
SELECT mship.id, m.id, m.full_name,
       COALESCE(NULLIF(m.whatsapp,''), m.contact_phone,''),
       COALESCE(mship.plan_id, 0), mship.facility_id,
       COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), ''), COALESCE(af.logo_url,''),
       COALESCE(mship.sport_id, 0),
       COALESCE(mship.plan_name,''), COALESCE(mship.team_name,''),
       COALESCE(mship.subscription_fee, 0), mship.primary_member
FROM memberships mship
JOIN members m ON m.id = mship.member_id
LEFT JOIN app_facilities af ON af.id = mship.facility_id
LEFT JOIN facilities f ON f.id = mship.facility_id
WHERE mship.status = 'active' AND %s = $1`, match)
	args := []any{last10}
	if facilityID > 0 {
		q += ` AND mship.facility_id = $2`
		args = append(args, facilityID)
	}
	q += ` ORDER BY COALESCE(NULLIF(af.name,''), NULLIF(f.name,''), ''), mship.plan_name, mship.id DESC`

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plans := []map[string]any{}
	for rows.Next() {
		var membershipID, memberID, planID, fid, sportID int64
		var mname, wa, fname, logo, planName, team string
		var fee float64
		var primary bool
		if err := rows.Scan(&membershipID, &memberID, &mname, &wa, &planID, &fid, &fname, &logo,
			&sportID, &planName, &team, &fee, &primary); err != nil {
			return nil, err
		}
		if planID <= 0 {
			planID = membershipID
		}
		if strings.TrimSpace(planName) == "" {
			planName = "Membership"
		}
		plans = append(plans, map[string]any{
			"membershipId": membershipID, "memberId": memberID, "memberName": mname,
			"whatsappNumber": wa, "planId": planID,
			"facilityId": fid, "facilityName": fname, "facilityLogoUrl": logo,
			"sportId": sportID, "sportName": "",
			"planName": planName, "subFacilityName": "", "slotTime": "",
			"indiv_price": 0.0, "grp_price": 0.0, "subscription_type": "individual",
			"max_member_count": 1, "current_member_count": 1,
			"teamName": team, "subscriptionFee": fee,
			"isPrimary": primary, "primaryMember": primary,
		})
	}
	return plans, rows.Err()
}

func (s *Service) appMemberPlansByUserOrWA(ctx context.Context, uid int64, last10 string) ([]map[string]any, error) {
	waCol := phoneLast10SQL("p.whatsapp_number")
	uCol := phoneLast10SQL("u.whatsapp_no")
	q := fmt.Sprintf(`
SELECT p.id, p.membership_id, p.member_id, p.member_name, p.plan_id, p.facility_id,
       COALESCE(f.name,''), COALESCE(f.logo_url,''), p.sport_id, p.sport_name, p.plan_name,
       p.sub_facility_name, p.slot_time, p.indiv_price, p.grp_price, p.subscription_type,
       p.max_member_count, p.current_member_count, p.team_name, p.subscription_fee, p.is_primary
FROM app_member_plans p
LEFT JOIN app_facilities f ON f.id=p.facility_id
LEFT JOIN app_users u ON u.id = p.user_id
WHERE p.user_id=$1 OR %s = $2 OR (%s = $2 AND $2 <> '')
ORDER BY p.id DESC`, waCol, uCol)

	rows, err := s.DB.QueryContext(ctx, q, uid, last10)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, mid, memberID, planID, fid, sportID int64
		var mname, fname, logo, sportName, planName, sub, slot, subType, team string
		var indiv, grp, fee float64
		var maxC, curC int
		var primary bool
		if err := rows.Scan(&id, &mid, &memberID, &mname, &planID, &fid, &fname, &logo, &sportID, &sportName, &planName,
			&sub, &slot, &indiv, &grp, &subType, &maxC, &curC, &team, &fee, &primary); err != nil {
			return nil, err
		}
		if mid == 0 {
			mid = id
		}
		if planID <= 0 {
			planID = mid
		}
		if strings.TrimSpace(planName) == "" {
			planName = "Membership"
		}
		list = append(list, map[string]any{
			"membershipId": mid, "memberId": memberID, "memberName": mname, "planId": planID,
			"facilityId": fid, "facilityName": fname, "facilityLogoUrl": logo,
			"sportId": sportID, "sportName": sportName, "planName": planName,
			"subFacilityName": sub, "slotTime": slot, "indiv_price": indiv, "grp_price": grp,
			"subscription_type": subType, "max_member_count": maxC, "current_member_count": curC,
			"teamName": team, "subscriptionFee": fee, "isPrimary": primary, "primaryMember": primary,
		})
	}
	return list, rows.Err()
}

func (s *Service) primaryMembersMyPlans(ctx context.Context, body map[string]any) (bool, string, any) {
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	uid, _ := parseInt64Field(body, "userId")
	wa := s.resolvePortalWhatsApp(ctx, body)
	facilityID, _ := parseInt64Field(body, "facilityId")

	var list []map[string]any
	var err error

	if s.tableExists(ctx, "memberships") {
		list, err = s.liveMyPlans(ctx, last10, facilityID)
		if err != nil {
			return false, err.Error(), nil
		}
	}
	if len(list) == 0 {
		list, err = s.appMemberPlansByUserOrWA(ctx, uid, last10)
		if err != nil {
			return false, err.Error(), nil
		}
	}

	msg := "OK"
	if len(list) == 0 {
		msg = "No memberships found."
	}
	return true, msg, map[string]any{
		"whatsappNo": wa,
		"plans":      list,
		"count":      len(list),
	}
}

func planIDFromBody(body map[string]any) int64 {
	for _, k := range []string{"planId", "plan_id", "membershipId", "membership_id"} {
		if v, ok := body[k]; ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(v)), 10, 64)
			if n > 0 {
				return n
			}
		}
	}
	return 0
}

func (s *Service) primaryMembersPlanMembers(ctx context.Context, body map[string]any) (bool, string, any) {
	planID := planIDFromBody(body)
	if planID <= 0 {
		return false, "planId is required.", nil
	}
	last10, errMsg := s.resolvePortalLast10(ctx, body)
	if errMsg != "" {
		return false, errMsg, nil
	}
	uid, _ := parseInt64Field(body, "userId")
	wa := s.resolvePortalWhatsApp(ctx, body)

	if s.tableExists(ctx, "memberships") {
		ok, msg, data := s.livePlanMembers(ctx, planID, last10, wa)
		if ok {
			return ok, msg, data
		}
		if msg != "" && data == nil {
			return ok, msg, data
		}
	}

	// Fallback: app_member_plans cache + legacy proxy.
	mid := planID
	rows, err := s.DB.QueryContext(ctx, `
SELECT membership_id, member_id, member_name, whatsapp_number, is_primary, user_id= $2 AS is_mine,
       status_label, coming_today FROM app_member_plans
WHERE plan_id=$1 OR membership_id=$1 OR id=$1`, mid, uid)
	if err == nil {
		defer rows.Close()
		members := []map[string]any{}
		myIDs := []int64{}
		coming := 0
		viewerPrimary := false
		for rows.Next() {
			var membershipID, memberID int64
			var mname, waNum, status string
			var primary, isMine, comingToday bool
			_ = rows.Scan(&membershipID, &memberID, &mname, &waNum, &primary, &isMine, &status, &comingToday)
			if comingToday {
				coming++
			}
			if isMine {
				myIDs = append(myIDs, membershipID)
				if primary {
					viewerPrimary = true
				}
			}
			members = append(members, map[string]any{
				"membershipId": membershipID, "memberId": memberID, "memberName": mname,
				"whatsappNumber": waNum, "isPrimary": primary, "isMine": isMine,
				"statusLabel": status, "canSelect": true, "request_status": "",
				"comingToday": comingToday, "canToggleComing": isMine || viewerPrimary,
			})
		}
		if len(members) > 0 {
			return true, "OK", map[string]any{
				"plan": map[string]any{"planId": planID}, "members": members,
				"myMembershipIds": myIDs, "isViewerPrimary": viewerPrimary,
				"viewerWhatsapp": wa, "playDate": time.Now().Format("2006-01-02"),
				"comingTodayCount": coming,
			}
		}
	}

	return true, "OK", map[string]any{
		"plan": map[string]any{"planId": planID}, "members": []any{},
		"myMembershipIds": []int64{}, "isViewerPrimary": false,
		"playDate": time.Now().Format("2006-01-02"), "comingTodayCount": 0,
	}
}

func (s *Service) livePlanMembers(ctx context.Context, planID int64, last10, viewerWA string) (bool, string, any) {
	myIDs, err := s.liveMyMembershipIDs(ctx, last10, planID)
	if err != nil {
		return false, err.Error(), nil
	}
	isPrimary, err := s.liveViewerIsPrimaryOnPlan(ctx, last10, planID)
	if err != nil {
		return false, err.Error(), nil
	}

	rows, err := s.DB.QueryContext(ctx, `
SELECT mship.id, m.id, m.full_name,
       COALESCE(NULLIF(m.whatsapp,''), m.contact_phone,''),
       mship.primary_member, COALESCE(mship.team_name,''), COALESCE(mship.subscription_fee, 0),
       mship.plan_id, COALESCE(mship.plan_name,''), mship.facility_id,
       COALESCE(mship.request_status,''), COALESCE(mship.request_box,'')
FROM memberships mship
JOIN members m ON m.id = mship.member_id
WHERE (mship.plan_id = $1 OR mship.id = $1) AND mship.status = 'active'
ORDER BY mship.primary_member DESC, mship.id DESC`, planID)
	if err != nil {
		return false, err.Error(), nil
	}
	defer rows.Close()

	mySet := map[int64]struct{}{}
	for _, id := range myIDs {
		mySet[id] = struct{}{}
	}

	members := []map[string]any{}
	var planMeta map[string]any
	for rows.Next() {
		var membershipID, memberID, pid, fid int64
		var mname, waNum, team, planName, reqStatus, reqBox string
		var fee float64
		var primary bool
		if err := rows.Scan(&membershipID, &memberID, &mname, &waNum, &primary, &team, &fee, &pid, &planName, &fid, &reqStatus, &reqBox); err != nil {
			return false, err.Error(), nil
		}
		if planMeta == nil {
			planMeta = map[string]any{
				"planId": planID, "planName": planName, "facilityId": fid,
				"subFacilityName": "", "slotTime": "",
				"indiv_price": 0.0, "grp_price": 0.0, "subscription_type": "",
				"max_member_count": 0, "current_member_count": 0,
			}
		}
		_, isMine := mySet[membershipID]
		statusLabel := ""
		req := strings.ToLower(strings.TrimSpace(reqStatus))
		if req == "pending" {
			statusLabel = "PENDING REQUEST"
		} else if !isPrimary {
			statusLabel = "VIEW ONLY"
		}
		canSelect := isPrimary && !isMine && !primary && req != "pending"
		members = append(members, map[string]any{
			"membershipId": membershipID, "memberId": memberID, "memberName": mname,
			"whatsappNumber": waNum, "primaryMember": primary, "isPrimary": primary,
			"isMine": isMine, "teamName": team, "subscriptionFee": fee,
			"statusLabel": statusLabel, "canSelect": canSelect, "request_status": reqStatus,
			"request_box": reqBox,
			"comingToday": false, "canToggleComing": isMine,
		})
	}
	if err := rows.Err(); err != nil {
		return false, err.Error(), nil
	}
	if len(members) == 0 {
		return false, "", nil
	}
	if planMeta == nil {
		planMeta = map[string]any{"planId": planID}
	}
	playDate := time.Now().Format("2006-01-02")
	coming := s.enrichLivePlanMembersComing(ctx, members, playDate)
	return true, "OK", map[string]any{
		"plan": planMeta, "members": members,
		"myMembershipIds": myIDs, "isViewerPrimary": isPrimary,
		"viewerWhatsapp": viewerWA, "playDate": playDate,
		"comingTodayCount": coming,
	}
}
