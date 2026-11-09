CREATE TRIGGER IF NOT EXISTS assets_task_links_insert AFTER INSERT ON assets BEGIN
 INSERT INTO task_asset_links(task_id,asset_id,source,source_summary)
 SELECT t.id,NEW.id,'legacy','任务资产关联' FROM tasks t JOIN json_each(NEW.task_ids) j ON t.id=j.value
 WHERE true ON CONFLICT DO NOTHING;
END;
CREATE TRIGGER IF NOT EXISTS assets_task_links_update AFTER UPDATE OF task_ids ON assets BEGIN
 DELETE FROM task_asset_links WHERE asset_id=NEW.id AND task_id NOT IN (SELECT value FROM json_each(NEW.task_ids));
 INSERT INTO task_asset_links(task_id,asset_id,source,source_summary)
 SELECT t.id,NEW.id,'legacy','任务资产关联' FROM tasks t JOIN json_each(NEW.task_ids) j ON t.id=j.value
 WHERE true ON CONFLICT DO NOTHING;
END;
CREATE TRIGGER IF NOT EXISTS conversation_retest_delete BEFORE DELETE ON conversations BEGIN
 UPDATE finding_retests SET status='stopped',error='复测会话已删除',finished_at=strftime('%Y-%m-%d %H:%M:%f','now')
 WHERE conversation_id=OLD.id AND status IN ('pending','running');
END;
CREATE TRIGGER IF NOT EXISTS case_content_changed AFTER UPDATE ON findings BEGIN
 UPDATE finding_cases SET version=version+1 WHERE id IN (SELECT case_id FROM finding_case_members WHERE finding_id=NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS case_member_deleted AFTER DELETE ON finding_case_members BEGIN
 UPDATE finding_cases SET version=version+1 WHERE id=OLD.case_id;
 INSERT INTO finding_case_events(case_id,action,actor,reason,finding_ids)
 SELECT OLD.case_id,'member_deleted','system','成员移出或删除',json_array(OLD.finding_id)
 WHERE EXISTS(SELECT 1 FROM finding_cases WHERE id=OLD.case_id);
 UPDATE finding_cases SET active=false WHERE id=OLD.case_id AND (SELECT count(*) FROM finding_case_members WHERE case_id=OLD.case_id)<2;
 DELETE FROM finding_case_members WHERE case_id=OLD.case_id AND EXISTS(SELECT 1 FROM finding_cases WHERE id=OLD.case_id AND NOT active);
END;
CREATE TRIGGER IF NOT EXISTS suggestion_reanchor BEFORE DELETE ON findings BEGIN
 UPDATE finding_case_suggestions SET
 left_id=(SELECT min(finding_id) FROM finding_case_suggestion_members WHERE suggestion_id=finding_case_suggestions.id AND finding_id<>OLD.id),
 right_id=(SELECT max(finding_id) FROM finding_case_suggestion_members WHERE suggestion_id=finding_case_suggestions.id AND finding_id<>OLD.id)
 WHERE (left_id=OLD.id OR right_id=OLD.id) AND (SELECT count(*) FROM finding_case_suggestion_members WHERE suggestion_id=finding_case_suggestions.id AND finding_id<>OLD.id)>=2;
END;
