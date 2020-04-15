package checkpoint

import (
	"redis-shake/common"
	redigo "github.com/garyburd/redigo/redis"
	"fmt"
	"strconv"
	"strings"
	"pkg/libs/log"
)

func LoadCheckpoint(dbSyncerId int, sourceAddr string, target []string, authType, passwd string, isCluster bool, tlsEnable bool) (string, int64, int, error) {
	c := utils.OpenRedisConn(target, authType, passwd, isCluster, tlsEnable)

	// fetch logical db list
	ret, err := c.Do("info", "keyspace")
	if err != nil {
		return "", 0, 0, err
	}

	// TODO, for some kind of redis type, like codis, tencent cloud, the keyspace result may not be accurate
	// so there maybe some problems.
	mp, err := utils.ParseKeyspace(ret.([]byte))
	if err != nil {
		return "", 0, 0, err
	}

	var newestOffsetBeg int64 = -1
	var newestOffsetEnd int64 = -1
	var recRunId string
	var recDb int32
	for db := range mp {
		log.Infof("DbSyncer[%d] load checkpoint check db[%v]", dbSyncerId, db)
		runId, offsetBegin, offsetEnd, err := fetchCheckpoint(sourceAddr, c, int(db))
		if err != nil {
			return "", 0, 0, err
		}

		// in some cases, begin offset or end offset may be empty
		if offsetBegin > newestOffsetBeg || offsetEnd > newestOffsetEnd {
			if offsetBegin > newestOffsetBeg {
				newestOffsetBeg = offsetBegin
			}
			if offsetEnd > newestOffsetEnd {
				newestOffsetEnd = offsetEnd
			}
			recRunId = runId
			recDb = db
		}

		if offsetBegin != offsetEnd {
			log.Warnf("DbSyncer[%d] db[%v] offsetBegin[%v] != offsetEnd[%v]", dbSyncerId, db, offsetBegin,
				offsetEnd)
		}
	}

	log.Infof("DbSyncer[%d] newestOffsetBeg[%v], newestOffsetEnd[%v], recordDb[%v]", dbSyncerId, newestOffsetBeg,
		newestOffsetEnd, recDb)
	if newestOffsetBeg != newestOffsetEnd {
		log.Warnf("DbSyncer[%d] offset check failed, need full sync", dbSyncerId)
		if err := ClearCheckpoint(dbSyncerId, c, -1, mp, sourceAddr); err != nil {
			log.Warnf("clear old checkpoint failed[%v]", err)
		}
		return "?", -1, 0, nil
	} else {
		if err := ClearCheckpoint(dbSyncerId, c, recDb, mp, sourceAddr); err != nil {
			log.Warnf("DbSyncer[%d] clear old checkpoint failed[%v]", dbSyncerId, err)
		}
		return recRunId, newestOffsetBeg, int(recDb), nil
	}
}

/*
 * fetch checkpoint from give address
 * @return:
 *     string: runid
 *     int64: offset-beginint
 *     int64: offset-end
 *     error
 */
func fetchCheckpoint(sourceAddr string, c redigo.Conn, db int) (string, int64, int64, error) {
	_, err := c.Do("select", db)
	if err != nil {
		return "", -1, -1, fmt.Errorf("fetch checkpoint do select db[%v] failed[%v]", db, err)
	}

	// judge checkpoint exists
	if reply, err := c.Do("exists", utils.CheckpointKey); err != nil {
		return "", -1, -1, fmt.Errorf("fetch checkpoint do judge checkpoint exists failed[%v]", err)
	} else {
		if reply.(int64) == 0 {
			// not exist
			return "", -1, -1, nil
		}
	}

	// hgetall
	if reply, err := c.Do("hgetall", utils.CheckpointKey); err != nil {
		return "", -1, -1, fmt.Errorf("fetch checkpoint do hgetall failed[%v]", err)
	} else {
		runId := "?"
		var offsetBegin int64 = -1
		var offsetEnd int64 = -1
		replyList := reply.([]interface{})
		for i := 0; i < len(replyList); i += 2 {
			line := replyList[i].([]byte)
			lineS := utils.Bytes2String(line)
			if strings.HasPrefix(lineS, sourceAddr) {
				if strings.Contains(lineS, utils.CheckpointOffsetBegin) {
					next := utils.Bytes2String(replyList[i + 1].([]byte))
					offsetBegin, err = strconv.ParseInt(next, 10, 64)
					if err != nil {
						return "", -1, -1, fmt.Errorf("fetch checkpoint do parse offset-begin[%v] failed[%v]",
							next, err)
					}
				}

				if strings.Contains(lineS, utils.CheckpointOffsetEnd) {
					next := utils.Bytes2String(replyList[i + 1].([]byte))
					offsetEnd, err = strconv.ParseInt(next, 10, 64)
					if err != nil {
						return "", -1, -1, fmt.Errorf("fetch checkpoint do parse offset-end[%v] failed[%v]",
							next, err)
					}
				}

				if strings.Contains(lineS, utils.CheckpointRunId) {
					runId = utils.Bytes2String(replyList[i + 1].([]byte))
				}
			}
		}

		return runId, offsetBegin, offsetEnd, nil
	}
}

func ClearCheckpoint(dbSyncerId int, c redigo.Conn, exceptDb int32, dbKeyMap map[int32]int64, sourceAddr string) error {
	runId := fmt.Sprintf("%s-%s", sourceAddr, utils.CheckpointRunId)
	offsetBeg := fmt.Sprintf("%s-%s", sourceAddr, utils.CheckpointOffsetBegin)
	offsetEnd := fmt.Sprintf("%s-%s", sourceAddr, utils.CheckpointOffsetEnd)

	for db := range dbKeyMap {
		if db == exceptDb {
			continue
		}

		if _, err := c.Do("select", db); err != nil {
			return fmt.Errorf("do select db[%v] failed[%v]", db, err)
		}

		if ret, err := c.Do("hdel", utils.CheckpointKey, runId, offsetBeg, offsetEnd); err != nil {
			return err
		} else {
			log.Debugf("DbSyncer[%d] db[%v] remove checkpoint[%v] field[%v %v %v] with return[%v]",
				db, dbSyncerId, utils.CheckpointKey, runId, offsetBeg, offsetEnd, ret)
		}

		log.Infof("DbSyncer[%d] clear checkpoint of logical db[%v]", dbSyncerId, db)
	}
	return nil
}