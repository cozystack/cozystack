{{- define "incloud-web-resources.pod.icon" -}}
- type: antdFlex
  data:
    id: header-row
    gap: 6
    align: center
    # style:
    #   marginBottom: 24px
  children:
    - type: antdText
      data:
        id: header-badge
        text: P
        title: Pods
        style:
          fontSize: 20px
          lineHeight: 24px
          padding: "0 9px"
          borderRadius: "20px"
          minWidth: 24
          display: inline-block
          textAlign: center
          whiteSpace: nowrap
          color: "#fff"
          backgroundColor: "#009596"
          fontFamily: RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif
          fontWeight: 400
{{- end -}}

{{- define "incloud-web-resources.namespace.icon" -}}
- type: antdFlex
  data:
    id: header-row
    gap: 6
    align: center
    # style:
    #   marginBottom: 24px
  children:
    - type: antdText
      data:
        id: header-badge
        text: NS
        title: Nanesoace
        style:
          fontSize: 20px
          lineHeight: 24px
          padding: "0 9px"
          borderRadius: "20px"
          minWidth: 24
          display: inline-block
          textAlign: center
          whiteSpace: nowrap
          color: "#fff"
          backgroundColor: "#45a703ff"
          fontFamily: RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif
          fontWeight: 400
{{- end -}}


{{- define "incloud-web-resources.icon" -}}
{{- $text := (default "" .text) -}}
{{- $title := (default "" .title) -}}
{{- $backgroundColor := (default "#a25792ff" .backgroundColor) -}}
- type: antdFlex
  data:
    id: header-row
    gap: 6
    align: center
  children:
    # Badge with resource short name
    - type: antdText
      data:
        id: header-badge
        text: "{{ $text }}"
        title: "{{ $title }}"
        style:
          fontSize: 15px
          lineHeight: 24px
          padding: "0 9px"
          borderRadius: "20px"
          minWidth: 24
          display: inline-block
          textAlign: center
          whiteSpace: nowrap
          color: "#fff"
          backgroundColor: "{{ $backgroundColor }}"
          fontFamily: RedHatDisplay, Overpass, overpass, helvetica, arial, sans-serif
          fontWeight: 400
{{- end -}}
