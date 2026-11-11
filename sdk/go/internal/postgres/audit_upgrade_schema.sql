WITH relations AS MATERIALIZED (
 SELECT c.* FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='heimdall'
), signature AS (
 SELECT jsonb_build_object(
 'relations', (SELECT jsonb_agg(jsonb_build_array(c.relname,c.relkind,c.relpersistence,c.relrowsecurity,c.relforcerowsecurity,c.relispartition,a.amname) ORDER BY c.relname COLLATE "C") FROM relations c LEFT JOIN pg_catalog.pg_am a ON a.oid=c.relam),
 'columns', (SELECT jsonb_agg(jsonb_build_array(c.relname,a.attnum,a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attisdropped,a.attidentity,a.attgenerated,coll.collnamespace::regnamespace::text,coll.collname,pg_catalog.pg_get_expr(d.adbin,d.adrelid)) ORDER BY c.relname COLLATE "C",a.attnum) FROM relations c JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum LEFT JOIN pg_catalog.pg_collation coll ON coll.oid=a.attcollation WHERE c.relkind IN ('r','p','v','m','f')),
 'constraints', (SELECT jsonb_agg(jsonb_build_array(c.relname,k.conname,k.contype,k.convalidated,k.condeferrable,k.condeferred,k.connoinherit,pg_catalog.pg_get_constraintdef(k.oid,false)) ORDER BY c.relname COLLATE "C",k.conname COLLATE "C") FROM relations c JOIN pg_catalog.pg_constraint k ON k.conrelid=c.oid),
 'indexes', (SELECT jsonb_agg(jsonb_build_array(c.relname,pg_catalog.pg_get_indexdef(i.indexrelid),i.indisvalid,i.indisready,i.indislive,i.indimmediate,i.indisunique,i.indisprimary,i.indisexclusion,i.indnullsnotdistinct) ORDER BY c.relname COLLATE "C") FROM relations c JOIN pg_catalog.pg_index i ON i.indexrelid=c.oid),
 'triggers', (SELECT jsonb_agg(s.item ORDER BY s.item::text COLLATE "C") FROM (SELECT jsonb_build_array(c.relname,CASE WHEN t.tgisinternal THEN NULL ELSE t.tgname END,t.tgisinternal,t.tgenabled,t.tgtype,t.tgdeferrable,t.tginitdeferred,t.tgfoid::regprocedure::text,k.conname,t.tgattr::text,encode(t.tgargs,'hex'),pg_catalog.pg_get_expr(t.tgqual,t.tgrelid),t.tgoldtable,t.tgnewtable) AS item FROM relations c JOIN pg_catalog.pg_trigger t ON t.tgrelid=c.oid LEFT JOIN pg_catalog.pg_constraint k ON k.oid=t.tgconstraint) s),
 'functions', (SELECT jsonb_agg(pg_catalog.pg_get_functiondef(p.oid) ORDER BY p.proname COLLATE "C",p.oid::regprocedure::text COLLATE "C") FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='heimdall'),
 'sequences', (SELECT jsonb_agg(jsonb_build_array(c.relname,pg_catalog.format_type(s.seqtypid,NULL),s.seqstart,s.seqincrement,s.seqmax,s.seqmin,s.seqcache,s.seqcycle) ORDER BY c.relname COLLATE "C") FROM relations c JOIN pg_catalog.pg_sequence s ON s.seqrelid=c.oid),
 'rules', (SELECT count(*) FROM relations c JOIN pg_catalog.pg_rewrite r ON r.ev_class=c.oid),
 'policies', (SELECT count(*) FROM relations c JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid),
 'inheritance', (SELECT count(*) FROM pg_catalog.pg_inherits i WHERE i.inhrelid IN (SELECT oid FROM relations) OR i.inhparent IN (SELECT oid FROM relations))
 ) AS value
) SELECT value FROM signature
