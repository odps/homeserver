package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools de los envíos que se sacan del correo (envios.go).

type shipmentsIn struct {
	IncluirEntregados bool `json:"incluir_entregados,omitempty" jsonschema:"true para incluir también los entregados (se guardan 30 días)"`
}

type shipmentsOut struct {
	Envios []shipmentView `json:"envios"`
}

func addShipmentTools(server *mcp.Server, svc *service) {
	mcp.AddTool(server, &mcp.Tool{Name: "envios_ver", Description: "Paquetes y pedidos de la persona, sacados de su correo sin IA: transportista, número de seguimiento, tienda, estado (pedido confirmado, enviado, en reparto, incidencia, disponible para recoger o entregado), entrega prevista (AAAA-MM-DD) y el enlace de seguimiento que traía el correo. Por defecto, solo los pendientes; los primeros, los que llegan antes. Para ver un correo del envío, correo_leer con uno de sus ids"},
		func(ctx context.Context, req *mcp.CallToolRequest, in shipmentsIn) (*mcp.CallToolResult, shipmentsOut, error) {
			who, err := identity(req)
			if err != nil {
				return nil, shipmentsOut{}, err
			}
			l, err := svc.shipments(ctx, who.User, in.IncluirEntregados)
			return nil, shipmentsOut{Envios: l}, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "envios_borrar", Description: "Quita un envío de la lista por su id (ya ha llegado o no interesa); no vuelve a salir aunque lleguen más correos suyos. No borra ningún correo"},
		func(ctx context.Context, req *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, shipmentView, error) {
			who, err := identity(req)
			if err != nil {
				return nil, shipmentView{}, err
			}
			v, err := svc.hideShipment(ctx, who.User, in.ID)
			return nil, v, err
		})
}
