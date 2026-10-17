import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

/// Native guided flow card. All labels are advisory, not backend outcomes.
class WorkspaceExperience extends StatelessWidget {
  const WorkspaceExperience({
    required this.slug,
    required this.recordCount,
    required this.statusCount,
    required this.propertyScoped,
    super.key,
  });
  final String slug;
  final int recordCount, statusCount;
  final bool propertyScoped;

  // Product-specific stages; changes to an actual record remain driven by
  // the existing repository and transition engine, not by this card.
  static const _flows = <String, List<String>>{
    'identity':['Account & trust','Prepare profile','Review identity','Manage access','verification','Property verification'],
    'financial':['Financial controls','Review details','Set preferences','Inspect example ledger','rent','Rent & deposits'],
    'ownership':['Ownership governance','Record ownership','Request consent','Review authority','managers','Manager controls'],
    'properties':['Portfolio studio','Create property','Add units','Prepare listing','listings','Listing management'],
    'verification':['Property evidence','Choose property','Attach metadata','Review case','listings','Listings'],
    'listings':['Property marketing','Create draft','Set terms','Preview listing','pricing','Pricing'],
    'pricing':['Commercial terms','Choose listing','Set example price','Review costs','availability','Availability'],
    'availability':['Availability control','Select unit','Block dates','Review conflicts','shortlets','Short-lets'],
    'inquiries':['Prospect pipeline','Review inquiry','Qualify lead','Book viewing','viewings','Viewings'],
    'viewings':['Property visits','Offer slots','Confirm visit','Record outcome','applications','Applications'],
    'applications':['Tenant screening','Review applicant','Inspect permitted evidence','Record decision','tenancies','Agreements'],
    'tenancies':['Agreement lifecycle','Draft agreement','Review signers','Coordinate move-in','rent','Rent'],
    'rent':['Rent & deposits','Review obligations','Observe demo payment','Resolve deposit','renewals','Renewals'],
    'renewals':['Tenancy continuity','Review expiry','Draft notice','Close tenancy','tenancies','Tenancies'],
    'utilities':['Property outgoings','Review meters','Record usage','Allocate charges','reports','Reports'],
    'commercial':['Commercial property','Define terms','Review offer','Track milestone','sales','Sales'],
    'land':['Land portfolio','Record parcel','Review documents','Track offer','sales','Sales'],
    'sales':['Sales & closing','Review offer','Complete diligence','Track closing','ownership','Ownership'],
    'shortlets':['Guest operations','Configure rates','Review booking','Complete turnover','availability','Availability'],
    'managers':['Delegated operations','Propose manager','Set permissions','Review access','ownership','Ownership'],
    'maintenance':['Property care','Report issue','Compare quote','Review completion','professionals','Professionals'],
    'professionals':['Expert services','Define service','Arrange appointment','Review findings','maintenance','Maintenance'],
    'notifications':['Owner inbox','Review alert','Open record','Update preferences','settings','Settings'],
    'reports':['Portfolio intelligence','Select filters','Review insights','Export demo data','financial','Finance'],
    'safety':['Trust & privacy','Create case','Review evidence','Track resolution','settings','Settings'],
    'settings':['Personal workspace','Set preferences','Review assistance','Confirm changes','identity','Account'],
    'financing':['Partner-dependent','Review information','Understand limits','Return later','financial','Finance'],
    'insurance':['Protection records','View example policy','Review expiry','Check limits','properties','Properties'],
    'access':['Connected property','Select unit','Review demo grant','Revoke access','viewings','Viewings'],
  };

  @override
  Widget build(BuildContext context) {
    final flow = _flows[slug];
    if (flow == null) return const SizedBox.shrink();
    final scheme = Theme.of(context).colorScheme;
    final dangerZone = ['financing','insurance','access','sales','ownership','verification','financial'].contains(slug);
    final icon = switch (slug) {
      'properties' || 'listings' || 'land' || 'commercial' => Icons.apartment_outlined,
      'viewings' || 'availability' || 'shortlets' || 'renewals' => Icons.calendar_month_outlined,
      'financial' || 'rent' || 'pricing' || 'reports' => Icons.account_balance_wallet_outlined,
      'maintenance' || 'professionals' || 'utilities' => Icons.handyman_outlined,
      'tenancies' || 'applications' || 'inquiries' => Icons.people_outline,
      _ => Icons.shield_outlined,
    };
    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(17, 20, 17, 17),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Row(children: [
            Container(
              width: 48, height: 48,
              decoration: BoxDecoration(
                color: scheme.primary.withValues(alpha:.1),
                borderRadius: BorderRadius.circular(14),
              ),
              child: Icon(icon, size:24, color:scheme.primary),
            ),
            const SizedBox(width:13),
            Expanded(child:Column(crossAxisAlignment:CrossAxisAlignment.start,children:[
              Text(flow[0].toUpperCase(),style:Theme.of(context).textTheme.labelSmall?.copyWith(
                color:scheme.primary,letterSpacing:1,fontWeight:FontWeight.w800,
              )),
              const SizedBox(height:5),
              Text('From overview to outcome',style:Theme.of(context).textTheme.titleMedium),
            ])),
          ]),
          const SizedBox(height:12),
          Text(
            dangerZone
              ? 'Explore connected demo records and review states. External financial, legal, identity or hardware operations are not performed.'
              : 'Manage the complete local demonstration journey, from first record to a clear next action.',
            style:Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height:13),
          Wrap(spacing:7,runSpacing:7,children:[
            Chip(label:Text('$recordCount local records')),
            Chip(label:Text('$statusCount status types')),
            if(propertyScoped)const Chip(label:Text('Property scoped')),
          ]),
          const SizedBox(height:13),
          Container(
            padding:const EdgeInsets.all(14),
            decoration:BoxDecoration(
              borderRadius:BorderRadius.circular(13),
              color:scheme.primary.withValues(alpha:.045),
              border:Border.all(color:scheme.outlineVariant),
            ),
            child:Column(crossAxisAlignment:CrossAxisAlignment.stretch,children:[
              Text('RECOMMENDED JOURNEY',style:Theme.of(context).textTheme.labelSmall?.copyWith(
                color:scheme.primary,letterSpacing:.65,
              )),
              const SizedBox(height:9),
              ...[1,2,3].map((i)=>Padding(
                padding:const EdgeInsets.only(bottom:7),
                child:Row(children:[
                  CircleAvatar(
                    radius:13,backgroundColor:scheme.surface,
                    child:Text('$i',style:Theme.of(context).textTheme.labelSmall?.copyWith(color:scheme.primary)),
                  ),
                  const SizedBox(width:9),
                  Expanded(child:Text(flow[i],style:Theme.of(context).textTheme.bodyMedium)),
                ]),
              )),
              TextButton.icon(
                onPressed:()=>context.push('/workspace/'+flow[4]),
                icon:const Icon(Icons.arrow_forward_rounded,size:17),
                label:Text('Continue to '+flow[5]),
              ),
            ]),
          ),
        ]),
      ),
    );
  }
}
