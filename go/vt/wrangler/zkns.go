package zkwrangler

import (
	"fmt"
	"path"

	"code.google.com/p/vitess/go/jscfg"
	"code.google.com/p/vitess/go/vt/naming"
	tm "code.google.com/p/vitess/go/vt/tabletmanager"
	"code.google.com/p/vitess/go/zk"
	"code.google.com/p/vitess/go/zk/zkns"
	"launchpad.net/gozk/zookeeper"
)

// Export addresses from the VT serving graph to a legacy zkns server.
func (wr *Wrangler) ExportZkns(zkVtRoot string) error {
	vtNsPath := path.Join(zkVtRoot, "ns")
	zkCell := zk.ZkCellFromZkPath(zkVtRoot)
	zknsRootPath := fmt.Sprintf("/zk/%v/zkns/vt", zkCell)

	children, err := zk.ChildrenRecursive(wr.zconn, vtNsPath)
	if err != nil {
		return err
	}

	for _, child := range children {
		addrPath := path.Join(vtNsPath, child)
		zknsAddrPath := path.Join(zknsRootPath, child)
		_, stat, err := wr.zconn.Get(addrPath)
		if err != nil {
			return err
		}
		// Leaf nodes correspond to zkns vdns files in the old setup.
		if stat.NumChildren() > 0 {
			continue
		}

		if err = wr.exportVtnsToZkns(addrPath, zknsAddrPath); err != nil {
			return err
		}
	}
	return nil
}

// Export addresses from the VT serving graph to a legacy zkns server.
func (wr *Wrangler) ExportZknsForKeyspace(zkKeyspacePath string) error {
	vtRoot := tm.VtRootFromKeyspacePath(zkKeyspacePath)
	keyspace := path.Base(zkKeyspacePath)
	shardNames, _, err := wr.zconn.Children(path.Join(zkKeyspacePath, "shards"))
	if err != nil {
		return err
	}

	// Scan the first shard to discover which cells need local serving data.
	zkShardPath := tm.ShardPath(vtRoot, keyspace, shardNames[0])
	aliases, err := tm.FindAllTabletAliasesInShard(wr.zconn, zkShardPath)
	if err != nil {
		return err
	}

	cellMap := make(map[string]bool)
	for _, alias := range aliases {
		cellMap[alias.Cell] = true
	}

	for cell, _ := range cellMap {
		vtnsRootPath := fmt.Sprintf("/zk/%v/vt/ns/%v", cell, keyspace)
		zknsRootPath := fmt.Sprintf("/zk/%v/zkns/vt/%v", cell, keyspace)

		children, err := zk.ChildrenRecursive(wr.zconn, vtnsRootPath)
		if err != nil {
			return err
		}

		for _, child := range children {
			vtnsAddrPath := path.Join(vtnsRootPath, child)
			zknsAddrPath := path.Join(zknsRootPath, child)

			_, stat, err := wr.zconn.Get(vtnsAddrPath)
			if err != nil {
				return err
			}
			// Leaf nodes correspond to zkns vdns files in the old setup.
			if stat.NumChildren() > 0 {
				continue
			}
			if err = wr.exportVtnsToZkns(vtnsAddrPath, zknsAddrPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (wr *Wrangler) exportVtnsToZkns(vtnsAddrPath, zknsAddrPath string) error {
	addrs, err := naming.ReadAddrs(wr.zconn, vtnsAddrPath)
	if err != nil {
		return err
	}

	// Write the individual endpoints and compute the SRV entries.
	vtoccAddrs := LegacyZknsAddrs{make([]string, 0, 8)}
	defaultAddrs := LegacyZknsAddrs{make([]string, 0, 8)}
	for i, entry := range addrs.Entries {
		zknsAddrPath := fmt.Sprintf("%v/%v", zknsAddrPath, i)
		zknsAddr := zkns.ZknsAddr{Host: entry.Host, Port: entry.NamedPortMap["_mysql"], NamedPortMap: entry.NamedPortMap}
		err := WriteAddr(wr.zconn, zknsAddrPath, &zknsAddr)
		if err != nil {
			return err
		}
		defaultAddrs.Endpoints = append(defaultAddrs.Endpoints, zknsAddrPath)
		vtoccAddrs.Endpoints = append(vtoccAddrs.Endpoints, zknsAddrPath+":_vtocc")
	}

	// Prune any zkns entries that are no longer referenced by the
	// shard graph.
	deleteIdx := len(addrs.Entries)
	for {
		zknsAddrPath := fmt.Sprintf("%v/%v", zknsAddrPath, deleteIdx)
		err := wr.zconn.Delete(zknsAddrPath, -1)
		if zookeeper.IsError(err, zookeeper.ZNONODE) {
			break
		}
		if err != nil {
			return err
		}
		deleteIdx++
	}

	// Write the VDNS entries for both vtocc and mysql
	vtoccVdnsPath := fmt.Sprintf("%v/_vtocc.vdns", zknsAddrPath)
	if err = WriteAddrs(wr.zconn, vtoccVdnsPath, &vtoccAddrs); err != nil {
		return err
	}

	defaultVdnsPath := fmt.Sprintf("%v.vdns", zknsAddrPath)
	if err = WriteAddrs(wr.zconn, defaultVdnsPath, &defaultAddrs); err != nil {
		return err
	}
	return nil
}

type LegacyZknsAddrs struct {
	Endpoints []string `json:"endpoints"`
}

func WriteAddr(zconn zk.Conn, zkPath string, addr *zkns.ZknsAddr) error {
	data := jscfg.ToJson(addr)
	_, err := zk.CreateOrUpdate(zconn, zkPath, data, 0, zookeeper.WorldACL(zookeeper.PERM_ALL), true)
	return err
}

func WriteAddrs(zconn zk.Conn, zkPath string, addrs *LegacyZknsAddrs) error {
	data := jscfg.ToJson(addrs)
	_, err := zk.CreateOrUpdate(zconn, zkPath, data, 0, zookeeper.WorldACL(zookeeper.PERM_ALL), true)
	return err
}
